# Vendored third-party assets

These files are vendored so that `codewalk serve` works entirely offline and a
`go install` of codewalk is self-contained. Nothing here is loaded from a CDN at
runtime — the local server never makes outbound requests on behalf of the UI.

| File              | Version | License | Source                                             |
| ----------------- | ------- | ------- | -------------------------------------------------- |
| `mermaid.min.js`  | 11.x    | MIT     | https://github.com/mermaid-js/mermaid              |

To refresh, run `scripts/fetch-web-vendor.sh`, then review the diff before
committing.
