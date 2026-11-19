package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountHandlerGetModelDegradationProbeModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/admin/accounts/model-degradation-probe/models", handler.GetModelDegradationProbeModels)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/model-degradation-probe/models", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			Models       []string `json:"models"`
			DefaultModel string   `json:"default_model"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, service.ModelDegradationProbeDefaultModel, response.Data.DefaultModel)
	require.Contains(t, response.Data.Models, response.Data.DefaultModel)
	require.Contains(t, response.Data.Models, "gpt-6.2-astra")
}

func TestAccountHandlerSetModelDegradationProbePauseValidatesRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetModelDegradationProbeService(service.NewModelDegradationProbeService(nil, nil, nil))
	router := gin.New()
	router.PUT("/admin/accounts/:id/model-degradation-probe/pause", handler.SetModelDegradationProbePauseEnabled)

	for name, tc := range map[string]struct {
		path string
		body string
	}{
		"missing enabled": {"/admin/accounts/1/model-degradation-probe/pause", `{}`},
		"invalid id":      {"/admin/accounts/abc/model-degradation-probe/pause", `{"enabled":true}`},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}
