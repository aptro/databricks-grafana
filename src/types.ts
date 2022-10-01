import { DataQuery, DataSourceJsonData } from '@grafana/data';

export interface MyQuery extends DataQuery {
  timeColumnName: string;
  valueColumnName: string;
  whereQuery?: string;
  tableName: string;
  withStreaming: boolean;
}

export const defaultQuery: Partial<MyQuery> = {
  timeColumnName: "timestamp",
  valueColumnName: "value",
  withStreaming: false,
};

/**
 * These are options configured for each DataSource instance.
 */
export interface MyDataSourceOptions extends DataSourceJsonData {
  path?: string;
}

/**
 * Value that is used in the backend, but never sent over HTTP to the frontend
 */
export interface MySecureJsonData {
  hostname?: string;
  path?: string;
  token?: string;
}

export interface MyVariableQuery {
  namespace: string;
  rawQuery: string;
}
