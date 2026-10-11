export function isStructuredMetadataRequest(payload) {
  return Array.isArray(payload.tools)
    && payload.tools.length === 0
    && payload.output_config?.format?.type === 'json_schema';
}
