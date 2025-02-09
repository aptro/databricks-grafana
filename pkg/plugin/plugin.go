package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/databricks/databricks-sql-go"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/grafana/grafana-plugin-sdk-go/data/sqlutil"
	"reflect"
	"strings"
	"time"
)

// Make sure Datasource implements required interfaces. This is important to do
// since otherwise we will only get a not implemented error response from plugin in
// runtime. In this example datasource instance implements backend.QueryDataHandler,
// backend.CheckHealthHandler, backend.StreamHandler interfaces. Plugin should not
// implement all these interfaces - only those which are required for a particular task.
// For example if plugin does not need streaming functionality then you are free to remove
// methods that implement backend.StreamHandler. Implementing instancemgmt.InstanceDisposer
// is useful to clean up resources used by previous datasource instance when a new datasource
// instance created upon datasource settings changed.
var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
	_ instancemgmt.InstanceDisposer = (*Datasource)(nil)
	_ backend.CallResourceHandler   = (*Datasource)(nil)
)

type DatasourceSettings struct {
	Path     string `json:"path"`
	Hostname string `json:"hostname"`
	Port     string `json:"port"`
}

// NewSampleDatasource creates a new datasource instance.
func NewSampleDatasource(settings backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	datasourceSettings := new(DatasourceSettings)
	err := json.Unmarshal(settings.JSONData, datasourceSettings)
	if err != nil {
		log.DefaultLogger.Info("Setting Parse Error", "err", err)
	}
	port := "443"
	if datasourceSettings.Port != "" {
		port = datasourceSettings.Port
	}
	databricksConnectionsString := fmt.Sprintf("token:%s@%s:%s/%s", settings.DecryptedSecureJSONData["token"], datasourceSettings.Hostname, port, datasourceSettings.Path)
	databricksDB := &sql.DB{}
	if databricksConnectionsString != "" {
		log.DefaultLogger.Info("Init Databricks SQL DB")
		db, err := sql.Open("databricks", databricksConnectionsString)
		if err != nil {
			log.DefaultLogger.Info("DB Init Error", "err", err)
		} else {
			databricksDB = db
			databricksDB.SetConnMaxIdleTime(6 * time.Hour)
			log.DefaultLogger.Info("Store Databricks SQL DB Connection")
		}
	}

	return &Datasource{
		databricksConnectionsString: databricksConnectionsString,
		databricksDB:                databricksDB,
	}, nil
}

// Datasource is an example datasource which can respond to data queries, reports
// its health and has streaming skills.
type Datasource struct {
	databricksConnectionsString string
	databricksDB                *sql.DB
}

func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	return autocompletionQueries(req, sender, d.databricksDB)
}

// Dispose here tells plugin SDK that plugin wants to clean up resources when a new instance
// created. As soon as datasource settings change detected by SDK old datasource instance will
// be disposed and a new one will be created using NewSampleDatasource factory function.
func (d *Datasource) Dispose() {
	// Clean up datasource instance resources.
}

// QueryData handles multiple queries and returns multiple responses.
// req contains the queries []DataQuery (where each query contains RefID as a unique identifier).
// The QueryDataResponse contains a map of RefID to the response for each query, and each response
// contains Frames ([]*Frame).
func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	log.DefaultLogger.Info("QueryData called", "request", req)

	// create response struct
	response := backend.NewQueryDataResponse()

	// loop over queries and execute them individually.
	for _, q := range req.Queries {
		res := d.query(ctx, req.PluginContext, q)

		// save the response in a hashmap
		// based on with RefID as identifier
		response.Responses[q.RefID] = res
	}

	return response, nil
}

type querySettings struct {
	ConvertLongToWide bool          `json:"convertLongToWide"`
	FillMode          data.FillMode `json:"fillMode"`
	FillValue         float64       `json:"fillValue"`
}

type queryModel struct {
	RawSqlQuery   string        `json:"rawSqlQuery"`
	QuerySettings querySettings `json:"querySettings"`
}

func (d *Datasource) query(_ context.Context, pCtx backend.PluginContext, query backend.DataQuery) backend.DataResponse {
	response := backend.DataResponse{}

	// Unmarshal the JSON into our queryModel.
	var qm queryModel

	log.DefaultLogger.Info("Query Ful", "query", query)
	err := json.Unmarshal(query.JSON, &qm)
	if err != nil {
		response.Error = err
		log.DefaultLogger.Info("Query Parsing Error", "err", err)
		return response
	}

	queryString := replaceMacros(qm.RawSqlQuery, query)

	// Check if multiple statements are present in the query
	// If so, split them and execute them individually
	if strings.Contains(queryString, ";") {
		// Split the query string into multiple statements
		queries := strings.Split(queryString, ";")
		// Check if the last statement is empty or just whitespace and newlines
		if strings.TrimSpace(queries[len(queries)-1]) == "" {
			// Remove the last statement
			queries = queries[:len(queries)-1]
		}
		// Check if there are stil multiple statements
		if len(queries) > 1 {
			// Execute all but the last statement without returning any data
			for _, query := range queries[:len(queries)-1] {
				_, err := d.databricksDB.Exec(query)
				if err != nil {
					response.Error = err
					log.DefaultLogger.Info("Error", "err", err)
					return response
				}
			}
			// Set the query string to the last statement
			queryString = queries[len(queries)-1]
		}
	}

	log.DefaultLogger.Info("Query", "query", queryString)

	frame := data.NewFrame("response")

	rows, err := d.databricksDB.Query(queryString)
	if err != nil {
		response.Error = err
		log.DefaultLogger.Info("Error", "err", err)
		return response
	}

	dateConverter := sqlutil.Converter{
		Name:          "Databricks date to timestamp converter",
		InputScanType: reflect.TypeOf(sql.NullString{}),
		InputTypeName: "DATE",
		FrameConverter: sqlutil.FrameConverter{
			FieldType: data.FieldTypeNullableTime,
			ConverterFunc: func(n interface{}) (interface{}, error) {
				v := n.(*sql.NullString)

				if !v.Valid {
					return (*time.Time)(nil), nil
				}

				f := v.String
				date, error := time.Parse("2006-01-02", f)
				if error != nil {
					return (*time.Time)(nil), error
				}
				return &date, nil
			},
		},
	}

	frame, err = sqlutil.FrameFromRows(rows, -1, dateConverter)
	if err != nil {
		log.DefaultLogger.Info("FrameFromRows", "err", err)
		response.Error = err
		return response
	}

	if qm.QuerySettings.ConvertLongToWide {
		wideFrame, err := data.LongToWide(frame, &data.FillMissing{Value: qm.QuerySettings.FillValue, Mode: qm.QuerySettings.FillMode})
		if err != nil {
			log.DefaultLogger.Info("LongToWide conversion error", "err", err)
		} else {
			frame = wideFrame
		}

	}

	// add the frames to the response.
	response.Frames = append(response.Frames, frame)

	return response
}

// CheckHealth handles health checks sent from Grafana to the plugin.
// The main use case for these health checks is the test button on the
// datasource configuration page which allows users to verify that
// a datasource is working as expected.
func (d *Datasource) CheckHealth(_ context.Context, req *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	log.DefaultLogger.Info("CheckHealth called", "request", req)

	dsn := d.databricksConnectionsString

	if dsn == "" {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "No connection string found." + "Set the DATABRICKS_DSN environment variable, and try again.",
		}, nil
	}

	rows, err := d.databricksDB.Query("SELECT 1")

	if err != nil {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: fmt.Sprintf("SQL Connection Failed: %s", err),
		}, nil
	}

	defer rows.Close()

	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusOk,
		Message: "Data source is working",
	}, nil
}
