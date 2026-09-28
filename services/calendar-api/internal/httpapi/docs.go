package httpapi

import (
	"net/http"

	"bscalendar/api"
)

// The documentation pages load their viewers from jsDelivr, so they get a looser CSP
// than the API responses (which allow nothing).
const docsCSP = "default-src 'none'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com data:; img-src 'self' data: https://cdn.jsdelivr.net; " +
	"connect-src 'self'; worker-src blob:; frame-ancestors 'none'"

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) error {
	writeBytes(w, r, http.StatusOK, "application/yaml; charset=utf-8", api.OpenAPI, "public, max-age=300", "")
	return nil
}

// docsBarCSS styles the header shared by the reference and try-it pages (matches the website).
const docsBarCSS = `  <style>
    body{margin:0;font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
    .bar{position:sticky;top:0;z-index:100;display:flex;align-items:center;gap:18px;height:56px;padding:0 20px;
      background:#fff;border-bottom:1px solid #e3e7ee}
    .bar .logo{display:flex;align-items:center;gap:10px;font-weight:700;color:#111827;text-decoration:none;white-space:nowrap}
    .bar .mark{display:inline-grid;place-items:center;width:30px;height:30px;border-radius:9px;background:#c2261d;color:#fff}
    .bar nav{margin-left:auto;display:flex;gap:16px;font-size:14px}
    .bar nav a{color:#566070;text-decoration:none}.bar nav a:hover,.bar nav a.on{color:#111827}
    @media (max-width:600px){.bar nav a.opt{display:none}}
  </style>`

const redocPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>BS Calendar API · Reference</title>
` + docsBarCSS + `
</head>
<body>
  <header class="bar"><a class="logo" href="/"><span class="mark">बि</span> BS Calendar API</a><nav><a href="/docs">Guide</a><a class="on" href="/docs/reference">Reference</a><a href="/docs/try">Try it</a><a class="opt" href="/">Home</a></nav></header>
  <redoc spec-url="/openapi.yaml" hide-download-button="false" native-scrollbars scroll-y-offset=".bar"
    theme='{"colors":{"primary":{"main":"#c2261d"}},"typography":{"fontFamily":"system-ui, -apple-system, Segoe UI, Roboto, sans-serif","headings":{"fontFamily":"system-ui, -apple-system, Segoe UI, Roboto, sans-serif"},"code":{"fontFamily":"ui-monospace, SFMono-Regular, Consolas, monospace"}},"sidebar":{"backgroundColor":"#f5f7fa"},"rightPanel":{"backgroundColor":"#0f172a"}}'></redoc>
  <script src="https://cdn.jsdelivr.net/npm/redoc@2/bundles/redoc.standalone.js"></script>
</body>
</html>`

const swaggerPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>BS Calendar API · Try it</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
` + docsBarCSS + `
  <style>.swagger-ui .info .title{color:#111827}.swagger-ui .btn.execute{background:#c2261d;border-color:#c2261d}</style>
</head>
<body>
  <header class="bar"><a class="logo" href="/"><span class="mark">बि</span> BS Calendar API</a><nav><a href="/docs">Guide</a><a href="/docs/reference">Reference</a><a class="on" href="/docs/try">Try it</a><a class="opt" href="/">Home</a></nav></header>
  <div id="ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({ url: "/openapi.yaml", dom_id: "#ui", deepLinking: true,
      persistAuthorization: true, tryItOutEnabled: true, displayRequestDuration: true });
  </script>
</body>
</html>`

func (s *Server) docsRedoc(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Security-Policy", docsCSP)
	w.Header().Set("X-Frame-Options", "DENY")
	writeBytes(w, r, http.StatusOK, "text/html; charset=utf-8", []byte(redocPage), "public, max-age=300", "")
	return nil
}

func (s *Server) docsSwagger(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Security-Policy", docsCSP)
	writeBytes(w, r, http.StatusOK, "text/html; charset=utf-8", []byte(swaggerPage), "public, max-age=300", "")
	return nil
}
