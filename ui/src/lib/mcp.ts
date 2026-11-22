// awxnext's MCP server (roadmap M12-03): its URL, and the command that adds
// it to Claude Code.

/** The MCP server's URL at the UI's origin: every tool, or one toolset's. */
export function mcpURL(origin: string, toolset = ''): string {
  return origin.replace(/\/+$/, '') + '/api/mcp/' + (toolset ? toolset + '/' : '')
}

/** The command that adds the MCP server at url to Claude Code, with a
 * personal access token, or a placeholder for one. */
export function claudeMcpAdd(url: string, token = '<token>'): string {
  return `claude mcp add --transport http awxnext ${url} --header "Authorization: Bearer ${token}"`
}
