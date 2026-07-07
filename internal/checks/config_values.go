package checks

// SQL engine tokens accepted by the sql check's engine field.
const (
	SQLEngineMariaDB    = "mariadb"
	SQLEngineMySQL      = "mysql"
	SQLEnginePostgres   = "postgres"
	SQLEnginePostgreSQL = "postgresql"
	SQLEngineSQLite     = "sqlite"
	SQLEngineSQLite3    = "sqlite3"
)

// Influx language tokens accepted by the influxdb-query check's language field.
const (
	InfluxLanguageFlux     = "flux"
	InfluxLanguageInfluxQL = "influxql"
)
