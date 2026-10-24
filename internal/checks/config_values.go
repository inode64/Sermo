package checks

// SQL engine tokens accepted by the sql check's engine field.
const (
	SQLEngineMariaDB  = "mariadb"
	SQLEngineMySQL    = "mysql"
	SQLEnginePostgres = "postgres"
	SQLEngineSQLite   = "sqlite"
	// SQLEngineSummary is the user-facing list of accepted sql engine values.
	SQLEngineSummary = SQLEngineMySQL + ", " +
		SQLEngineMariaDB + ", " +
		SQLEnginePostgres + ", " +
		SQLEngineSQLite
)

// Influx language tokens accepted by the influxdb-query check's language field.
const (
	InfluxLanguageFlux     = "flux"
	InfluxLanguageInfluxQL = "influxql"
	// InfluxLanguageSummary is the user-facing list of accepted InfluxDB query languages.
	InfluxLanguageSummary = InfluxLanguageInfluxQL + " or " + InfluxLanguageFlux
)
