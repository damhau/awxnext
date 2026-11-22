-- How an activity stream entry's change was made: 'mcp' through the MCP
-- server's tools, '' through the API or the UI.
ALTER TABLE activity_stream ADD COLUMN via text NOT NULL DEFAULT '';
