# Vendored page libraries

- `chart.umd.js` — Chart.js 4.4.7 (MIT, `chart.js-LICENSE.md`), the `dist/chart.umd.js`
  of the npm package `chart.js@4.4.7` (registry integrity
  `sha512-pwkcKfdzTMAU/+jNosKhNL2bHtJc/sSmYgVbuGTEDhzkrhmyihmP7vUc/5ZK9WopidMDHNe3Wm7jOd/WhuHWuw==`,
  verified at vendoring on 2026-10-18; file sha256
  `2812cb8825fdc57469eb2f7bb055e9429244e599920511ee477e828499b632cb`). Served by the
  web process at `/static/vendor/chart.umd.js` so the pages load no CDN (stage 2 of the
  admin-console program, B6/D-12) and the Content-Security-Policy names one script source.
  To upgrade: fetch the new tarball from the npm registry, verify `dist.integrity`, copy
  `package/dist/chart.umd.js` and `package/LICENSE.md`, update this file and
  `scripts/vendored_assets_test.go`'s hash.
