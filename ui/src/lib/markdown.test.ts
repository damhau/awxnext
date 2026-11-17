import { describe, expect, it } from 'vitest'
import { inlines, parseMarkdown, plainText, safeHref, slug } from './markdown'

describe('README blocks', () => {
  it('parses headings, paragraphs, rules and code', () => {
    expect(parseMarkdown('# Title\n\nSome text\non two lines.\n\n---\n\n```yaml\n- name: x\n```\n\n    indented\n    code\n')).toEqual([
      { t: 'heading', level: 1, children: [{ t: 'text', text: 'Title' }] },
      { t: 'para', children: [{ t: 'text', text: 'Some text on two lines.' }] },
      { t: 'rule' },
      { t: 'code', lang: 'yaml', text: '- name: x' },
      { t: 'code', lang: '', text: 'indented\ncode' },
    ])
  })
  it('parses setext headings', () => {
    expect(parseMarkdown('Title\n=====\nSub\n---\n')).toEqual([
      { t: 'heading', level: 1, children: [{ t: 'text', text: 'Title' }] },
      { t: 'heading', level: 2, children: [{ t: 'text', text: 'Sub' }] },
    ])
  })
  it('parses lists, nested and ordered', () => {
    expect(parseMarkdown('- one\n- two\n  - inner\n\n3. three\n4. four\n')).toEqual([
      {
        t: 'list',
        ordered: false,
        start: 1,
        items: [
          [{ t: 'para', children: [{ t: 'text', text: 'one' }] }],
          [
            { t: 'para', children: [{ t: 'text', text: 'two' }] },
            { t: 'list', ordered: false, start: 1, items: [[{ t: 'para', children: [{ t: 'text', text: 'inner' }] }]] },
          ],
        ],
      },
      {
        t: 'list',
        ordered: true,
        start: 3,
        items: [[{ t: 'para', children: [{ t: 'text', text: 'three' }] }], [{ t: 'para', children: [{ t: 'text', text: 'four' }] }]],
      },
    ])
  })
  it('parses quotes and tables', () => {
    expect(parseMarkdown('> quoted\n> text\n\n| a | b |\n|---|:-:|\n| 1 | `2` |\n')).toEqual([
      { t: 'quote', children: [{ t: 'para', children: [{ t: 'text', text: 'quoted text' }] }] },
      {
        t: 'table',
        head: [[{ t: 'text', text: 'a' }], [{ t: 'text', text: 'b' }]],
        rows: [[[{ t: 'text', text: '1' }], [{ t: 'code', text: '2' }]]],
      },
    ])
  })
  it('keeps a fenced block open to the end', () => {
    expect(parseMarkdown('```\nno end')).toEqual([{ t: 'code', lang: '', text: 'no end' }])
  })
})

describe('README inlines', () => {
  it('parses emphasis, code and escapes', () => {
    expect(inlines('**bold** and *em* and _em_ and ~~gone~~ and `a*b` and \\*not\\*')).toEqual([
      { t: 'strong', children: [{ t: 'text', text: 'bold' }] },
      { t: 'text', text: ' and ' },
      { t: 'em', children: [{ t: 'text', text: 'em' }] },
      { t: 'text', text: ' and ' },
      { t: 'em', children: [{ t: 'text', text: 'em' }] },
      { t: 'text', text: ' and ' },
      { t: 'del', children: [{ t: 'text', text: 'gone' }] },
      { t: 'text', text: ' and ' },
      { t: 'code', text: 'a*b' },
      { t: 'text', text: ' and *not*' },
    ])
  })
  it('leaves underscores inside words', () => {
    expect(inlines('snake_case_name')).toEqual([{ t: 'text', text: 'snake_case_name' }])
  })
  it('makes links, and drops unsafe targets', () => {
    expect(inlines('[docs](https://docs.example.com "Docs") <https://a.example.com> https://b.example.com.')).toEqual([
      { t: 'link', href: 'https://docs.example.com', children: [{ t: 'text', text: 'docs' }] },
      { t: 'text', text: ' ' },
      { t: 'link', href: 'https://a.example.com', children: [{ t: 'text', text: 'https://a.example.com' }] },
      { t: 'text', text: ' ' },
      { t: 'link', href: 'https://b.example.com', children: [{ t: 'text', text: 'https://b.example.com' }] },
      { t: 'text', text: '.' },
    ])
    expect(inlines('[click](javascript:alert(1)) <b>raw</b>')).toEqual([{ t: 'text', text: 'click' }, { t: 'text', text: ') <b>raw</b>' }])
  })
  it('shows images as links to them', () => {
    expect(inlines('![logo](https://example.com/logo.png)')).toEqual([{ t: 'link', href: 'https://example.com/logo.png', children: [{ t: 'text', text: 'logo' }] }])
    expect(inlines('![](data:image/png;base64,AAAA)')).toEqual([{ t: 'em', children: [{ t: 'text', text: 'data:image/png;base64,AAAA' }] }])
  })
  it('breaks lines on two trailing spaces', () => {
    expect(inlines('one  \ntwo')).toEqual([{ t: 'text', text: 'one' }, { t: 'br' }, { t: 'text', text: 'two' }])
  })
})

describe('anchors', () => {
  it('accepts only safe link targets', () => {
    expect(safeHref('https://x.example.com')).toBe('https://x.example.com')
    expect(safeHref('mailto:a@example.com')).toBe('mailto:a@example.com')
    expect(safeHref('#usage')).toBe('#usage')
    expect(safeHref('javascript:alert(1)')).toBeNull()
    expect(safeHref(' JaVaScRiPt:alert(1)')).toBeNull()
    expect(safeHref('docs/usage.md')).toBeNull()
  })
  it('makes GitHub’s heading anchors', () => {
    expect(slug(plainText(inlines('Using `acme.net` (v2)!')))).toBe('using-acmenet-v2')
  })
})
