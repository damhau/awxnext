import { parse as parseYaml, stringify as stringifyYaml } from 'yaml'
import { tr } from './i18n'

/** A credential type's inputs or injectors, edited as YAML or JSON. */
export type Spec = Record<string, unknown>

/** The editor's text as an object: empty is {}, anything but a mapping is an
 * error (the API takes inputs and injectors as objects). */
export function parseSpec(text: string): { value?: Spec; error?: string } {
  if (!text.trim()) return { value: {} }
  let v: unknown
  try {
    v = parseYaml(text)
  } catch (e) {
    return { error: `Not valid YAML or JSON: ${(e as Error).message.split('\n')[0]}` }
  }
  if (v === null || v === undefined) return { value: {} }
  if (typeof v !== 'object' || Array.isArray(v)) return { error: tr('Must be a mapping (keys and values), not a list or a single value.') }
  return { value: v as Spec }
}

/** An object as the editor's text: YAML, empty for {}. */
export function specText(v: unknown): string {
  if (!v || (typeof v === 'object' && Object.keys(v as object).length === 0)) return ''
  return stringifyYaml(v)
}

/** The ids of the input fields (inputs.fields[].id). */
export function fieldIds(inputs: Spec): string[] {
  const fields = Array.isArray(inputs.fields) ? inputs.fields : []
  return fields.map((f) => (f && typeof f === 'object' ? String((f as Spec).id ?? '') : '')).filter(Boolean)
}

/** Variables the injectors' templates use that no input field defines, to
 * point them out before saving (the API refuses them). tower.filename.* is
 * a file injector's path. */
export function undefinedRefs(inputs: Spec, injectors: Spec): string[] {
  const known = new Set(fieldIds(inputs))
  const found = new Set<string>()
  const walk = (v: unknown) => {
    if (typeof v === 'string') {
      for (const m of v.matchAll(/\{\{-?\s*([A-Za-z_][A-Za-z0-9_]*)/g)) {
        if (m[1] !== 'tower' && !known.has(m[1])) found.add(m[1])
      }
    } else if (v && typeof v === 'object') {
      for (const x of Object.values(v)) walk(x)
    }
  }
  walk(injectors)
  return [...found].sort()
}

export const inputsExample = `fields:
  - id: token
    label: API token
    type: string
    secret: true
  - id: region
    label: Region
    type: string
    default: eu-west-1
required:
  - token
`

export const injectorsExample = `env:
  MYCLOUD_TOKEN: '{{ token }}'
  MYCLOUD_REGION: '{{ region }}'
`
