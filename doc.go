// Package gmigrate applies versioned SQL and Go migrations and keeps a
// Flyway-compatible history table.
//
// It is an independent library: it does not depend on gable, gns or any other
// framework. Applications can use the Engine directly or the gmigrate CLI.
package gmigrate
