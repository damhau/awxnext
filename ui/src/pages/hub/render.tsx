import { Fragment, type CSSProperties, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ansibleMarkup, docLink, optionRows, paragraphs, type OptionRow } from '../../lib/hub'
import { tr } from '../../lib/i18n'
import { parseMarkdown, plainText, slug, type Block, type Inline } from '../../lib/markdown'

const external = { target: '_blank', rel: 'noopener noreferrer nofollow' }

const depthStyle = (depth: number) => ({ '--depth': depth }) as CSSProperties

function MdInlines({ xs }: { xs: Inline[] }) {
  return (
    <>
      {xs.map((x, i) => {
        switch (x.t) {
          case 'text':
            return <Fragment key={i}>{x.text}</Fragment>
          case 'code':
            return <code key={i}>{x.text}</code>
          case 'em':
            return (
              <em key={i}>
                <MdInlines xs={x.children} />
              </em>
            )
          case 'strong':
            return (
              <strong key={i}>
                <MdInlines xs={x.children} />
              </strong>
            )
          case 'del':
            return (
              <del key={i}>
                <MdInlines xs={x.children} />
              </del>
            )
          case 'br':
            return <br key={i} />
          case 'link':
            return x.href.startsWith('#') ? (
              <a key={i} href={'#readme-' + x.href.slice(1)}>
                <MdInlines xs={x.children} />
              </a>
            ) : (
              <a key={i} href={x.href} {...external}>
                <MdInlines xs={x.children} />
              </a>
            )
        }
        return null
      })}
    </>
  )
}

function MdBlocks({ blocks }: { blocks: Block[] }) {
  return (
    <>
      {blocks.map((b, i) => {
        switch (b.t) {
          case 'heading': {
            const H = `h${Math.min(b.level + 1, 6)}` as 'h2'
            return (
              <H key={i} id={'readme-' + slug(plainText(b.children))}>
                <MdInlines xs={b.children} />
              </H>
            )
          }
          case 'para':
            return (
              <p key={i}>
                <MdInlines xs={b.children} />
              </p>
            )
          case 'code':
            return (
              <pre key={i} className="code">
                {b.text}
              </pre>
            )
          case 'rule':
            return <hr key={i} />
          case 'quote':
            return (
              <blockquote key={i}>
                <MdBlocks blocks={b.children} />
              </blockquote>
            )
          case 'list': {
            const items = b.items.map((it, j) => {
              const first = it[0]
              return <li key={j}>{it.length === 1 && first.t === 'para' ? <MdInlines xs={first.children} /> : <MdBlocks blocks={it} />}</li>
            })
            return b.ordered ? (
              <ol key={i} start={b.start}>
                {items}
              </ol>
            ) : (
              <ul key={i}>{items}</ul>
            )
          }
          case 'table':
            return (
              <div key={i} className="table-wrap">
                <table className="data">
                  <thead>
                    <tr>
                      {b.head.map((c, j) => (
                        <th key={j}>
                          <MdInlines xs={c} />
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((r, j) => (
                      <tr key={j}>
                        {r.map((c, k) => (
                          <td key={k}>
                            <MdInlines xs={c} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
        }
        return null
      })}
    </>
  )
}

/** A README in Markdown, without its HTML. */
export function Markdown({ text }: { text: string }) {
  return (
    <div className="md">
      <MdBlocks blocks={parseMarkdown(text)} />
    </div>
  )
}

/** Where the docs of a collection's modules and plugins are shown, for
 * links between them. */
export interface DocContext {
  collection: string
  base: string
  search: string
}

/** A description in Ansible's documentation markup. */
export function AnsibleText({ text, ctx }: { text: string; ctx: DocContext }) {
  return (
    <>
      {ansibleMarkup(text).map((m, i) => {
        switch (m.kind) {
          case 'text':
            return <Fragment key={i}>{m.text}</Fragment>
          case 'code':
          case 'env':
            return <code key={i}>{m.text}</code>
          case 'value':
            return (
              <code key={i} className="doc-value">
                {m.text}
              </code>
            )
          case 'italic':
            return <em key={i}>{m.text}</em>
          case 'bold':
            return <b key={i}>{m.text}</b>
          case 'link':
            return (
              <a key={i} href={m.href} {...external}>
                {m.text}
              </a>
            )
          case 'module':
          case 'plugin': {
            const to = docLink(m.fqcn, m.type, ctx.collection, ctx.base)
            return to ? (
              <Link key={i} to={to + ctx.search}>
                <code>{m.fqcn}</code>
              </Link>
            ) : (
              <code key={i}>{m.fqcn}</code>
            )
          }
          case 'option':
          case 'return':
            return m.plugin ? (
              <code key={i} className="doc-option">
                {m.text}
              </code>
            ) : (
              <a key={i} href={`#${m.kind}-${m.name.replace(/\[[^\]]*\]/g, '')}`} className="doc-option-link">
                <code className="doc-option">{m.text}</code>
              </a>
            )
          case 'rule':
            return <hr key={i} />
        }
        return null
      })}
    </>
  )
}

function Paragraphs({ v, ctx }: { v: unknown; ctx: DocContext }) {
  return (
    <>
      {paragraphs(v).map((p, i) => (
        <p key={i}>
          <AnsibleText text={p} ctx={ctx} />
        </p>
      ))}
    </>
  )
}

function OptionsTable({ rows, kind, ctx }: { rows: OptionRow[]; kind: 'option' | 'return'; ctx: DocContext }) {
  return (
    <div className="table-wrap">
      <table className="data doc-options stack-sm">
        <thead>
          <tr>
            <th>{kind === 'option' ? tr('Parameter') : tr('Key')}</th>
            <th>{tr('Description')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.path} id={`${kind}-${r.path}`}>
              <td className="doc-param" style={depthStyle(r.depth)}>
                <div className="doc-name">
                  <b className="mono">{r.name}</b>
                  {r.required && <span className="tag doc-required">{tr('required')}</span>}
                </div>
                {r.type && <div className="faint small mono">{r.type}</div>}
                {r.aliases.length > 0 && (
                  <div className="faint small">
                    {tr('aliases:')} <span className="mono">{r.aliases.join(', ')}</span>
                  </div>
                )}
              </td>
              <td>
                {r.description.map((p, i) => (
                  <p key={i}>
                    <AnsibleText text={p} ctx={ctx} />
                  </p>
                ))}
                {r.choices.length > 0 && (
                  <div className="small">
                    {tr('Choices:')}{' '}
                    {r.choices.map((c, i) => (
                      <Fragment key={c}>
                        {i > 0 && ', '}
                        <code className={c === r.default ? 'doc-default' : undefined}>{c}</code>
                      </Fragment>
                    ))}
                    {r.default !== undefined && r.choices.includes(r.default) && <span className="faint"> {tr('(default: {value})', { value: r.default })}</span>}
                  </div>
                )}
                {r.default !== undefined && !r.choices.includes(r.default) && (
                  <div className="small">
                    {tr('Default:')} <code>{r.default}</code>
                  </div>
                )}
                {r.returned && (
                  <div className="small">
                    {tr('Returned:')} <AnsibleText text={r.returned} ctx={ctx} />
                  </div>
                )}
                {r.sample !== undefined && (
                  <div className="small">
                    {tr('Sample:')} <code className="doc-sample">{r.sample}</code>
                  </div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="doc-section">
      <h3>{title}</h3>
      {children}
    </section>
  )
}

type Doc = Record<string, unknown>

function asList(v: unknown): string[] {
  if (v === undefined || v === null || v === '') return []
  return Array.isArray(v) ? v.map((x) => (typeof x === 'string' ? x : JSON.stringify(x))) : [typeof v === 'string' ? v : JSON.stringify(v)]
}

function SeeAlso({ items, ctx }: { items: unknown; ctx: DocContext }) {
  if (!Array.isArray(items) || !items.length) return null
  return (
    <Section title={tr('See also')}>
      <ul className="doc-list">
        {items.map((raw, i) => {
          const s = (raw ?? {}) as Record<string, string>
          const target = s.module ?? s.plugin
          const description = s.description ? (
            <>
              {' '}
              — <AnsibleText text={s.description} ctx={ctx} />
            </>
          ) : null
          if (target) {
            const type = s.plugin_type ?? 'module'
            const to = docLink(target, type, ctx.collection, ctx.base)
            return (
              <li key={i}>
                {to ? (
                  <Link to={to + ctx.search}>
                    <code>{target}</code>
                  </Link>
                ) : (
                  <code>{target}</code>
                )}
                {description}
              </li>
            )
          }
          const href = /^https?:\/\//i.test(s.link ?? '') ? s.link : null
          return (
            <li key={i}>
              {href ? (
                <a href={href} {...external}>
                  {s.name ?? href}
                </a>
              ) : (
                <span>{s.name ?? s.ref ?? ''}</span>
              )}
              {description}
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

/** The body of a module's or plugin's docs, as ansible-doc reads them. */
function DocBody({ doc, ctx }: { doc: Doc; ctx: DocContext }) {
  const requirements = asList(doc.requirements)
  const notes = asList(doc.notes)
  const authors = asList(doc.author)
  const options = optionRows(doc.options)
  const deprecated = doc.deprecated as Record<string, string> | undefined
  return (
    <>
      {deprecated && (
        <div className="notice warn small">
          <b>{tr('Deprecated.')}</b> {deprecated.why ? <AnsibleText text={String(deprecated.why)} ctx={ctx} /> : null}{' '}
          {deprecated.alternative ? (
            <>
              {tr('Use instead:')} <AnsibleText text={String(deprecated.alternative)} ctx={ctx} />
            </>
          ) : null}
        </div>
      )}
      <Paragraphs v={doc.description} ctx={ctx} />
      {requirements.length > 0 && (
        <Section title={tr('Requirements')}>
          <ul className="doc-list">
            {requirements.map((r, i) => (
              <li key={i}>
                <AnsibleText text={r} ctx={ctx} />
              </li>
            ))}
          </ul>
        </Section>
      )}
      {options.length > 0 && (
        <Section title={tr('Parameters')}>
          <OptionsTable rows={options} kind="option" ctx={ctx} />
        </Section>
      )}
      {notes.length > 0 && (
        <Section title={tr('Notes')}>
          <ul className="doc-list">
            {notes.map((n, i) => (
              <li key={i}>
                <AnsibleText text={n} ctx={ctx} />
              </li>
            ))}
          </ul>
        </Section>
      )}
      <SeeAlso items={doc.seealso} ctx={ctx} />
      {authors.length > 0 && (
        <p className="small faint">
          {tr('Authors:')} {authors.join(', ')}
        </p>
      )}
    </>
  )
}

/** The docs of a module, plugin or role, as the API serves them. */
export function DocView({ type, doc, ctx }: { type: string; doc: Doc; ctx: DocContext }) {
  if (type === 'role') {
    const entries = Object.entries((doc.entry_points ?? {}) as Record<string, Doc>).sort(([a], [b]) => (a === 'main' ? -1 : b === 'main' ? 1 : a.localeCompare(b)))
    if (!entries.length) return <p className="faint">{tr('The role has no argument specs.')}</p>
    return (
      <>
        {entries.map(([name, ep]) => (
          <section key={name} className="doc-entry">
            <h3>
              {tr('Entry point')} <code>{name}</code>
              {ep.short_description ? <span className="muted"> — {String(ep.short_description)}</span> : null}
            </h3>
            <DocBody doc={ep} ctx={ctx} />
          </section>
        ))}
      </>
    )
  }
  const body = (doc.doc ?? {}) as Doc
  const examples = typeof doc.examples === 'string' ? doc.examples.replace(/^\n+/, '') : ''
  const returns = optionRows(doc.return)
  return (
    <>
      <DocBody doc={body} ctx={ctx} />
      {examples && (
        <Section title={tr('Examples')}>
          <pre className="code">{examples}</pre>
        </Section>
      )}
      {returns.length > 0 && (
        <Section title={tr('Return values')}>
          <OptionsTable rows={returns} kind="return" ctx={ctx} />
        </Section>
      )}
    </>
  )
}
