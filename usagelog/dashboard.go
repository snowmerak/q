package usagelog

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed web/dashboard.html web/dashboard.css web/dashboard.js openapi.json
var dashboardFiles embed.FS

var dashboardTemplate = template.Must(template.ParseFS(dashboardFiles, "web/dashboard.html"))

func serveDashboard(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTemplate.Execute(writer, struct{ Title string }{Title: "q usage"})
}

func serveDashboardCSS(writer http.ResponseWriter, _ *http.Request) {
	serveEmbedded(writer, "text/css; charset=utf-8", "web/dashboard.css")
}

func serveDashboardJS(writer http.ResponseWriter, _ *http.Request) {
	serveEmbedded(writer, "text/javascript; charset=utf-8", "web/dashboard.js")
}

func serveOpenAPI(writer http.ResponseWriter, _ *http.Request) {
	serveEmbedded(writer, "application/json; charset=utf-8", "openapi.json")
}

func serveEmbedded(writer http.ResponseWriter, contentType, name string) {
	body, err := dashboardFiles.ReadFile(name)
	if err != nil {
		http.Error(writer, "embedded asset unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	_, _ = writer.Write(body)
}
