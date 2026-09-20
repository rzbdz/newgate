package forward

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/porthub"
)

// TestDispatchPrefersMountedServices 锁「同一个端口上挂了别的服务」这件事：
// catch-all 先问挂载表，问不到才当数据面转发。
//
// 这是 porthub 的**接线**测试（表本身的行为在 lib/porthub 的单测里）。它要回答
// 两个问题：挂上去的服务真的能收到请求吗？撤掉之后同一条路径真的落回数据面吗？
// 后一半同样重要——「撤销之后还接着应答」是一条会让人以为 Stop 没生效的假象。
func TestDispatchPrefersMountedServices(t *testing.T) {
	isolate(t)
	s := newTestServer()

	const prefix = "/ui"
	rel, err := porthub.Default().Mount(prefix, "test-web",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("mounted"))
		}))
	if err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	defer rel()

	rec := httptest.NewRecorder()
	s.dispatch(rec, httptest.NewRequest(http.MethodGet, prefix+"/index.html", nil))
	if got := rec.Body.String(); got != "mounted" {
		t.Fatalf("挂在 %s 的服务没接到请求，实际响应: %q", prefix, got)
	}
	if rec.Header().Get("X-Newgate-Route") != "" {
		t.Error("挂了服务的路径不该被当数据面处理（那个响应头是数据面的）")
	}

	// 撤掉之后同一条路径必须落回数据面：测试里没有可用上游，所以答案是数据面的
	// 失败——关键是**不再**由那个 handler 应答。
	rel()
	rec2 := httptest.NewRecorder()
	s.dispatch(rec2, httptest.NewRequest(http.MethodGet, prefix+"/index.html", nil))
	if strings.Contains(rec2.Body.String(), "mounted") {
		t.Errorf("Release 之后仍被挂载的 handler 接住：%q", rec2.Body.String())
	}
}

// TestDispatchWithoutAnyMountGoesToTheProxy：表是空的（没人装 porthub 的消费者）
// 时，行为必须与加这个机制之前一模一样——数据面自己处理。这条是「关掉 porthub
// 之后一切照旧」的最小证据。
func TestDispatchWithoutAnyMountGoesToTheProxy(t *testing.T) {
	isolate(t)
	s := newTestServer()
	rec := httptest.NewRecorder()
	s.dispatch(rec, httptest.NewRequest(http.MethodGet, "/ui/index.html", nil))
	if rec.Body.String() == "" {
		t.Error("空表时 catch-all 该把请求交给数据面（数据面会给出它自己的答复）")
	}
	if rec.Header().Get("X-Newgate-Route") == "" && rec.Code == http.StatusOK {
		t.Error("没有上游时数据面不该给出 200——这条用例是来确认请求真的走到了数据面")
	}
}
