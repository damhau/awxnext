# Settings mockups (roadmap M8)

Mockups of the new settings pages, for approval before M8 is built
([#94](https://github.com/damhau/awxnext/issues/94)). Static HTML on the app's
own styles (`ui/src/styles.css`, its fonts and tokens), with the real settings,
labels written for them, and the app's shell around them.

| Page | File | Screenshot |
|---|---|---|
| Sign-in overview | [signin.html](signin.html) | [signin.png](screenshots/signin.png) |
| A login method: GitHub | [github.html](github.html) | [github.png](screenshots/github.png) |
| Jobs | [jobs.html](jobs.html) | [jobs.png](screenshots/jobs.png) |

`settings-mockup.css` holds what the pages add to the app's styles; it is
meant to become the settings page's stylesheet.

## What they show

- **Settings by task, not by AWX category.** The menu has System, Sign-in
  (with its login methods under it, each marked on or off), Jobs, Logging and
  Login page and UI, a search over every setting, and the settings changed
  from their default.
- **One row per setting:** what it does on the left, in plain words, with its
  AWX key below, small and click-to-copy; the control on the right. A setting
  changed from its default carries the amber "changed" cell, its default and a
  reset. Edits not saved yet are marked, and the page saves them at once.
- **Sign-in:** a card per login method with what it is set to. GitHub's six
  AWX categories are one form: where accounts come from, who may sign in,
  then the OAuth app (the callback URL to copy, the client ID, the secret
  shown as set) and the organization and team maps as rows instead of JSON.
- **Jobs:** launch safety first, set apart, each switch saying what turning it
  on lets a launcher or a project do; then limits and timeouts, content,
  execution, output and events. Durations have units and a "no limit" box;
  environment variables, mounted paths and modules are rows and chips.
- **Settings with no effect in awxnext** are hidden, with a link to show them.

## Viewing them

The pages load the app's CSS and fonts by relative path, so serve the
repository's root over HTTP (with `ui/node_modules` installed) and open them
there:

```sh
python3 -m http.server 8099
# http://localhost:8099/docs/awxnext-design/mockups/settings/signin.html
```
