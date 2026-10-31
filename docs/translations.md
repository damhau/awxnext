# Translations

The web UI is translatable (roadmap M7-16). It ships in English and French.
People pick a language in the account menu: their browser's, English or
Français. The choice is kept in the browser, and changing it reloads the
page.

What the API sends stays as the API sends it, in English: setting names and
help, validation errors, job explanations. So do names and descriptions
people gave their objects, and Ansible's output.

## In the code

Strings are keyed by their English text, gettext style:

```tsx
import { asIs, tr, trn } from '../lib/i18n'

tr('Delete {name}?', { name: inv.name })          // text with placeholders
trn(n, '{n} host', '{n} hosts')                    // a count and its noun
plural(n, 'host')                                  // the same, from lib/format
asIs('GitHub')                                     // a name: never translated
```

- **Placeholders** are `{name}`. Numbers are formatted for the locale.
- **Whole sentences, not fragments.** Write `tr('Runs {name} on {n} schedules.', …)`, not
  `'Runs ' + name + …`, so a language can reorder the words.
- **Dates and numbers** go through `lib/format.ts`: `ago`, `absolute`, `dateOnly`,
  `dateTimeText`, `upcoming`, and `number` from `lib/i18n`. They use the chosen locale.
- **Text built when a module loads** (section names, shortcut lists) may call `tr()`:
  `main.tsx` loads the catalog before it imports the app.

## The check

`ui/src/i18n.test.ts` runs with the UI's tests, in CI. It fails when:

- a display string is not translated. It looks at:
  - JSX text;
  - the attributes `label`, `title`, `help`, `placeholder`, `aria-label` and the like;
  - properties such as `header`, `label`, `title`, `body`, `confirm`;
  - `toast()` and `Error()` messages;
  - `[label, value]` pairs and `[value, 'Label']` options;
  - label maps;
  - any capitalised literal with a space.

  Wrap the string in `tr()`, or `asIs()` when it must stay as it is.
- a catalog lacks a string, or has one the UI no longer uses;
- a translation drops or renames a placeholder.

## Catalogs

`ui/src/locales/<locale>.json` maps each English string to its translation:

```json
{
  "Delete {name}?": "Supprimer {name} ?",
  "{n} hosts": { "one": "{n} hôte", "other": "{n} hôtes" }
}
```

Plurals are keyed by the English plural. Their forms are the
[Intl.PluralRules](https://developer.mozilla.org/docs/Web/JavaScript/Reference/Global_Objects/Intl/PluralRules)
categories of the language: `one` and `other` for French, where 0 and 1 are
singular; `one`, `few`, `many` and `other` for Polish, and so on. Each
catalog is a separate file of the build, loaded only for its language.

## Adding a language

1. Add the locale to `LOCALES` (named in its own language) and to `loaders`
   in `ui/src/lib/i18n.ts`.
2. List the strings to translate:

   ```sh
   cd ui
   I18N_REPORT=/tmp npx vitest run src/i18n.test.ts
   ```

   This writes `/tmp/keys.json`: every string, and for plurals their English forms.
3. Write `ui/src/locales/<locale>.json` with every key, and add it to `catalogs` in
   `ui/src/i18n.test.ts`.
4. Run `npx vitest run`: the check lists what is missing.

French conventions, for reference:

- buttons are infinitives ("Supprimer");
- AWX's own terms: tâche, modèle de tâche, flux de travail, inventaire, identifiant,
  planification;
- a non-breaking space goes before `? ! : ;` and inside « ».
