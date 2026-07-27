package com.folio.reader.bridge

import android.os.Handler
import android.os.Looper
import android.webkit.JavascriptInterface
import android.webkit.WebView
import com.folio.reader.AppFacade
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Wails-compatible bridge: frontend calls window.go.main.App.* which routes here.
 *
 * Protocol:
 *  - FolioAndroid.invoke(method, argsJsonArray, callbackId)
 *  - Native resolves via window.__folioResolve(id, json) / __folioReject(id, message)
 *  - File pickers: OpenFileDialog / RemapBookDialog request host UI asynchronously
 */
class FolioJsBridge(
    private val webView: WebView,
    private val app: AppFacade,
    private val host: HostActions
) {
    interface HostActions {
        fun pickBookFile(callbackId: String)
        fun pickRemapFile(bookId: String, callbackId: String)
    }

    private val main = Handler(Looper.getMainLooper())
    private val io = Executors.newFixedThreadPool(3)

    @JavascriptInterface
    fun invoke(method: String, argsJson: String, callbackId: String) {
        io.execute {
            try {
                when (method) {
                    "OpenFileDialog" -> {
                        host.pickBookFile(callbackId)
                        return@execute
                    }
                    "RemapBookDialog" -> {
                        val args = JSONArray(argsJson.ifBlank { "[]" })
                        val id = args.optString(0)
                        host.pickRemapFile(id, callbackId)
                        return@execute
                    }
                }
                val result = dispatch(method, JSONArray(argsJson.ifBlank { "[]" }))
                resolve(callbackId, result)
            } catch (e: Exception) {
                reject(callbackId, e.message ?: e.toString())
            }
        }
    }

    fun resolve(callbackId: String, value: Any?) {
        val json = when (value) {
            null -> "null"
            is JSONObject -> value.toString()
            is JSONArray -> value.toString()
            is String -> JSONObject.quote(value)
            is Number, is Boolean -> value.toString()
            else -> JSONObject.quote(value.toString())
        }
        val script =
            "window.__folioResolve && window.__folioResolve(${JSONObject.quote(callbackId)}, $json);"
        main.post { webView.evaluateJavascript(script, null) }
    }

    fun reject(callbackId: String, message: String) {
        val script =
            "window.__folioReject && window.__folioReject(${JSONObject.quote(callbackId)}, ${JSONObject.quote(message)});"
        main.post { webView.evaluateJavascript(script, null) }
    }

    fun emitEvent(name: String, payload: JSONObject) {
        val script =
            "window.__folioEmit && window.__folioEmit(${JSONObject.quote(name)}, ${payload});"
        main.post { webView.evaluateJavascript(script, null) }
    }

    private fun dispatch(method: String, args: JSONArray): Any? {
        return when (method) {
            "AppVersion" -> app.appVersion()
            "GetLibrary" -> app.getLibrary()
            "OpenPath" -> app.openPath(args.optString(0))
            "OpenBook" -> app.openBook(args.optString(0))
            "RemoveFromLibrary" -> {
                app.removeFromLibrary(args.optString(0)); null
            }
            "SaveProgress" -> {
                // Desktop may pass (page, chapter, sub, scroll) or older signatures
                val page = args.optInt(0, 0)
                val chapter = args.optInt(1, 0)
                val sub = if (args.length() >= 4) args.optInt(2, 0) else 0
                val scroll = when {
                    args.length() >= 4 -> args.optDouble(3, 0.0)
                    args.length() == 3 -> args.optDouble(2, 0.0)
                    args.length() == 2 -> args.optDouble(1, 0.0)
                    else -> 0.0
                }
                app.saveProgress(page, chapter, sub, scroll)
                null
            }
            "SaveBookProgress" -> {
                app.saveBookProgress(
                    args.optString(0),
                    args.optInt(1, 0),
                    args.optInt(2, 0),
                    args.optInt(3, 0),
                    args.optDouble(4, 0.0)
                )
                null
            }
            "GetProgress" -> app.getProgress()
            "GetBookProgress" -> app.getBookProgress(args.optString(0))
            "RenderCurrentPage" -> app.renderCurrentPage(args.optInt(0, 128))
            "RenderPDFPage" -> app.renderPdfPage(args.optInt(0, 0), args.optInt(1, 128), true)
            "PrefetchPDFPage" -> app.renderPdfPage(args.optInt(0, 0), args.optInt(1, 128), false)
            "PrefetchPDFPages" -> {
                val pagesArr = args.optJSONArray(0) ?: JSONArray()
                val pages = (0 until pagesArr.length()).map { pagesArr.optInt(it) }
                app.prefetchPdfPages(pages, args.optInt(1, 128))
                null
            }
            "GoToPage" -> app.goToPage(args.optInt(0, 0), args.optInt(1, 128))
            "NextPage" -> app.nextPage(args.optInt(0, 128))
            "PrevPage" -> app.prevPage(args.optInt(0, 128))
            "GetEPUBChapter" -> app.getEpubChapter(args.optInt(0, 0))
            "GetAllEPUBChapters" -> app.getAllEpubChapters()
            "GetEPUBChapterCount" -> app.getEpubChapterCount()
            "GetEPUBTOC" -> app.getEpubToc()
            "ResolveEPUBLink" -> app.resolveEpubLink(args.optString(0))
            "GetDocument" -> app.getDocument()
            "CloseDocument" -> {
                app.closeDocument(); null
            }
            "OpenExternalURL" -> {
                app.openExternalUrl(args.optString(0)); null
            }
            "GetOPDSSettings" -> app.getOpdsSettings()
            "SaveOPDSSettings" -> app.saveOpdsSettings(
                args.optString(0),
                args.optString(1),
                args.optString(2)
            )
            "OPDSOpenLibrary" -> app.opdsOpenLibrary()
            "OPDSSearch" -> app.opdsSearch(args.optString(0))
            "OPDSFetchPage" -> app.opdsFetchPage(args.optString(0))
            "OPDSDownload" -> app.opdsDownload(
                args.optString(0),
                args.optString(1),
                args.optString(2),
                args.optString(3)
            )
            else -> throw IllegalArgumentException("Unknown method: $method")
        }
    }

    companion object {
        /**
         * Bootstrap script injected on every page load to install window.go.main.App
         * and a minimal window.runtime Events API.
         */
        val BOOTSTRAP_JS: String = """
(function () {
  if (window.__folioBridgeInstalled) return;
  window.__folioBridgeInstalled = true;
  window.__folioSeq = 0;
  window.__folioCallbacks = {};
  window.__folioListeners = {};

  window.__folioResolve = function (id, value) {
    var cb = window.__folioCallbacks[id];
    if (!cb) return;
    delete window.__folioCallbacks[id];
    cb.resolve(value);
  };
  window.__folioReject = function (id, message) {
    var cb = window.__folioCallbacks[id];
    if (!cb) return;
    delete window.__folioCallbacks[id];
    cb.reject(new Error(message || 'error'));
  };
  window.__folioEmit = function (name, payload) {
    var list = window.__folioListeners[name] || [];
    for (var i = 0; i < list.length; i++) {
      try { list[i](payload); } catch (e) {}
    }
  };

  function call(method) {
    var args = Array.prototype.slice.call(arguments, 1);
    return new Promise(function (resolve, reject) {
      var id = String(++window.__folioSeq);
      window.__folioCallbacks[id] = { resolve: resolve, reject: reject };
      try {
        FolioAndroid.invoke(method, JSON.stringify(args), id);
      } catch (e) {
        delete window.__folioCallbacks[id];
        reject(e);
      }
    });
  }

  var methods = [
    'AppVersion','CloseDocument','GetDocument','GetEPUBChapter','GetAllEPUBChapters',
    'GetEPUBChapterCount','GetEPUBTOC','GetLibrary','GoToPage','NextPage','OpenBook',
    'OpenFileDialog','OpenPath','PrevPage','RemapBookDialog','RemoveFromLibrary',
    'RenderCurrentPage','RenderPDFPage','PrefetchPDFPage','PrefetchPDFPages',
    'SaveProgress','SaveBookProgress','GetProgress','GetBookProgress',
    'ResolveEPUBLink','OpenExternalURL','GetOPDSSettings','SaveOPDSSettings',
    'OPDSOpenLibrary','OPDSSearch','OPDSFetchPage','OPDSDownload'
  ];
  var App = {};
  methods.forEach(function (m) {
    App[m] = function () {
      var args = Array.prototype.slice.call(arguments);
      args.unshift(m);
      return call.apply(null, args);
    };
  });
  window.go = { main: { App: App } };

  window.runtime = {
    EventsOn: function (name, cb) {
      if (!window.__folioListeners[name]) window.__folioListeners[name] = [];
      window.__folioListeners[name].push(cb);
      return function () {
        var arr = window.__folioListeners[name] || [];
        var i = arr.indexOf(cb);
        if (i >= 0) arr.splice(i, 1);
      };
    },
    EventsOff: function (name) {
      delete window.__folioListeners[name];
    },
    EventsEmit: function () {},
    LogPrint: function () {},
    LogTrace: function () {},
    LogDebug: function () {},
    LogInfo: function () {},
    LogWarning: function () {},
    LogError: function () {},
    LogFatal: function () {},
    Quit: function () {},
    Environment: function () { return Promise.resolve({ platform: 'android' }); }
  };
})();
        """.trimIndent()
    }
}
