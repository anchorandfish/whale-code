package orchrecord

// schema.go — trace/quality 落盘格式的 schema 版本契约唯一权威。

// DefaultCompactSchemaVersion trace/quality 落盘 schema 的默认版本。
// 文件首行若无 _schema_version 字段，默认视为 "v1"（向后兼容 Phase 0/1 已有文件）。
//
// ★ 本常量是「版本契约」的唯一权威（附带必做项 i）：
//
//	消费者（memory）不再自持一份 DefaultCompactSchemaVersion 副本，
//	而是从本包派生 —— 生产者与消费者对「默认版本」的理解不会再悄然分裂。
const DefaultCompactSchemaVersion = "v1"

// SchemaVersionMarker 在 trace/quality JSONL 首行标记 schema 版本的字段名。
// 约定：JSONL 首行若是 {"_schema_version":"v1",...}，则取该版本；字段缺失则默认 "v1"。
//
// ★ 本常量同是唯一权威：生产者（orchestrator）与消费者（memory）对
//
//	「marker 字段叫什么」这一事实从本包取，不再各自写死字面量。
const SchemaVersionMarker = "_schema_version"
