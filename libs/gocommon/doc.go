// Package gocommon holds code shared by DevAssist's Go services (indexer and
// sandbox-runner): environment config, JSON logging with trace ids, the Kafka
// event envelope, health checks, and an HTTP server with graceful shutdown.
//
// It mirrors libs/devassist-common on the Python side so every service logs,
// reports health and speaks events the same way.
package gocommon
