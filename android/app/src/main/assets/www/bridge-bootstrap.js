/**
 * Folio Android — Wails-compatible bridge bootstrap.
 * Must load before main.js so hasWails() is true at boot.
 */
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
    cb.reject(new Error(message || "error"));
  };
  window.__folioEmit = function (name, payload) {
    var list = window.__folioListeners[name] || [];
    for (var i = 0; i < list.length; i++) {
      try {
        list[i](payload);
      } catch (e) {}
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
    "AppVersion",
    "CloseDocument",
    "GetDocument",
    "GetEPUBChapter",
    "GetAllEPUBChapters",
    "GetEPUBChapterCount",
    "GetEPUBTOC",
    "GetLibrary",
    "GoToPage",
    "NextPage",
    "OpenBook",
    "OpenFileDialog",
    "OpenPath",
    "PrevPage",
    "RemapBookDialog",
    "RemoveFromLibrary",
    "RenderCurrentPage",
    "RenderPDFPage",
    "PrefetchPDFPage",
    "PrefetchPDFPages",
    "SaveProgress",
    "SaveBookProgress",
    "GetProgress",
    "GetBookProgress",
    "ResolveEPUBLink",
    "OpenExternalURL",
    "GetOPDSSettings",
    "SaveOPDSSettings",
    "OPDSOpenLibrary",
    "OPDSSearch",
    "OPDSFetchPage",
    "OPDSDownload",
    "UploadLocalFile",
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
    Environment: function () {
      return Promise.resolve({ platform: "android" });
    },
  };
})();
