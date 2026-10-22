CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_created_at_cache_cover
    ON usage_logs (created_at)
    INCLUDE (account_id, input_tokens, cache_read_tokens, cache_creation_tokens, raw_input_tokens, raw_cache_read_tokens, actual_cost, cache_sim_cost_delta, image_count, image_output_tokens, image_input_tokens, video_count, inbound_endpoint, openai_ws_mode);
