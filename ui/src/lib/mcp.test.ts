import { describe, expect, it } from 'vitest'
import { claudeMcpAdd, mcpURL } from './mcp'

describe('the MCP server', () => {
  it('is at /api/mcp/ of the origin', () => {
    expect(mcpURL('https://awx.example.com')).toBe('https://awx.example.com/api/mcp/')
    expect(mcpURL('https://awx.example.com/')).toBe('https://awx.example.com/api/mcp/')
    expect(mcpURL('http://localhost:8053', 'job_management')).toBe('http://localhost:8053/api/mcp/job_management/')
  })
  it('is added to Claude Code with a bearer token', () => {
    expect(claudeMcpAdd('https://awx.example.com/api/mcp/')).toBe('claude mcp add --transport http awxnext https://awx.example.com/api/mcp/ --header "Authorization: Bearer <token>"')
    expect(claudeMcpAdd('https://awx.example.com/api/mcp/', 'abc123')).toBe('claude mcp add --transport http awxnext https://awx.example.com/api/mcp/ --header "Authorization: Bearer abc123"')
  })
})
