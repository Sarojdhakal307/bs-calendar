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

const redocPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>BS/AD Calendar API</title>
  <style>body{margin:0}</style>
</head>
<body>
  <redoc spec-url="/openapi.yaml" hide-download-button="false" native-scrollbars></redoc>
  <script src="https://cdn.jsdelivr.net/npm/redoc@2/bundles/redoc.standalone.js"></script>
</body>
</html>`

const swaggerPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>BS/AD Calendar API · Try it</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
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
