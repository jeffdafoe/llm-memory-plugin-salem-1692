-- LLM-947 down. The server-side preload is llm-memory-api's
-- conf.d/50-llm-memory-tuning.conf; this only drops the salem view.
DROP EXTENSION IF EXISTS pg_stat_statements;
