# Portal mockups (roadmap M15)

Mockups of the self-service portal, made while M15 was designed (Decision 21
in [ROADMAP.md](../../../ROADMAP.md)). Like the screens one level up, they are
design-tool sources: they don't render standalone, but the markup, the inline
styles and the data logic at the bottom of each file are the spec for layout,
spacing and copy. For behaviour, the issues are the spec; where a mockup and
an issue differ, the issue wins. Names, teams and data (Acme, Alex Morgan,
`orders-db-test`) are samples.

| Screen | File | Tasks |
|---|---|---|
| Catalog, the portal's home | [Catalog.dc.html](Catalog.dc.html) | M15-08 ([#167](https://github.com/damhau/awxnext/issues/167)) |
| Request form (a Linux VM) | [Request.dc.html](Request.dc.html) | M15-01, M15-04, M15-07, M15-08 |
| My requests | [Requests.dc.html](Requests.dc.html) | M15-08 |
| My deployments | [Deployments.dc.html](Deployments.dc.html) | M15-05, M15-06, M15-09 ([#183](https://github.com/damhau/awxnext/issues/183)) |
| Approvals | [Approvals.dc.html](Approvals.dc.html) | M15-04, M15-09 |
| Phone: the 2-hour expiry reminder (390×844) | [PhoneExpiry.dc.html](PhoneExpiry.dc.html) | M15-06, M15-09 |
| Phone: approve (390×844) | [PhoneApprove.dc.html](PhoneApprove.dc.html) | M15-09 |

## What they show

- **A shell of its own,** calmer than the admin UI: a light top bar with
  Catalog, My requests, My deployments and Approvals, a 15 px base, words for
  requesters, and an Admin view link only for people with admin roles. On a
  phone, the navigation moves to a bottom tab bar.
- **The heatmap cell, reused:** quota left (used, this request, free), a
  deployment's lifetime (one cell per day: gone, today, left, added), and a
  request's steps.
- **The catalog:** what needs you (an expiring deployment, a waiting request,
  approvals to give), categories, and on each item who approves and when, the
  quota left, and a change freeze with its end date.
- **The request form:** the item's questions, the fixed answers its owner
  chose to show, the lifetime, a reason when an approval applies, and a
  summary that says who approves and why before the request is sent.
- **My requests:** steps as cells, the playbook's plays as progress with a
  percentage, and what happens next for waiting, denied and failed requests.
- **My deployments:** expiry and extensions (+1 and +3 days at once, +7 days
  approved), how to connect with a password shown on demand (logged), day-2
  actions, history, who can use it, hand-over, deletion, and a teardown that
  failed.
- **Approvals:** the rule that applies (`size == "large"`), the requester's
  reason, the answer that decided it, the quota, the timeout; deny needs a
  comment, and nobody approves their own request.
