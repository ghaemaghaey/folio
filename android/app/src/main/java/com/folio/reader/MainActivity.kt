package com.folio.reader

import android.annotation.SuppressLint
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.view.ViewGroup
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsControllerCompat
import com.folio.reader.bridge.FolioJsBridge
import com.folio.reader.library.FolioPaths
import com.folio.reader.library.sanitizeFilename
import com.folio.reader.library.uniquePath
import java.io.File
import java.io.FileOutputStream

class MainActivity : AppCompatActivity(), FolioJsBridge.HostActions {

    private lateinit var webView: WebView
    private lateinit var app: AppFacade
    private lateinit var bridge: FolioJsBridge

    private var pendingPickCallbackId: String? = null
    private var pendingRemapBookId: String? = null
    /** For HTML &lt;input type="file"&gt; (account book upload). */
    private var filePathCallback: ValueCallback<Array<Uri>>? = null

    private val openBookLauncher = registerForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri: Uri? ->
        val cb = pendingPickCallbackId
        pendingPickCallbackId = null
        if (cb == null) return@registerForActivityResult
        if (uri == null) {
            bridge.resolve(cb, null)
            return@registerForActivityResult
        }
        Thread {
            try {
                val path = importUriToBooks(uri)
                val doc = app.openPath(path)
                bridge.resolve(cb, doc)
            } catch (e: Exception) {
                bridge.reject(cb, e.message ?: "Failed to open file")
            }
        }.start()
    }

    private val remapBookLauncher = registerForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri: Uri? ->
        val cb = pendingPickCallbackId
        val bookId = pendingRemapBookId
        pendingPickCallbackId = null
        pendingRemapBookId = null
        if (cb == null) return@registerForActivityResult
        if (uri == null || bookId.isNullOrBlank()) {
            bridge.resolve(cb, null)
            return@registerForActivityResult
        }
        Thread {
            try {
                val path = importUriToBooks(uri)
                val doc = app.remapBook(bookId, path)
                bridge.resolve(cb, doc)
            } catch (e: Exception) {
                bridge.reject(cb, e.message ?: "Failed to remap file")
            }
        }.start()
    }

    private val htmlFileChooserLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val cb = filePathCallback
        filePathCallback = null
        if (cb == null) return@registerForActivityResult
        val data = result.data
        val uris = WebChromeClient.FileChooserParams.parseResult(result.resultCode, data)
        cb.onReceiveValue(uris)
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, true)
        WindowInsetsControllerCompat(window, window.decorView).isAppearanceLightStatusBars = true

        webView = WebView(this).apply {
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT
            )
            setBackgroundColor(0xFFF0E6D2.toInt())
        }
        setContentView(FrameLayout(this).apply {
            setBackgroundColor(0xFFF0E6D2.toInt())
            addView(webView)
        })

        app = AppFacade(applicationContext) { name, payload ->
            if (::bridge.isInitialized) bridge.emitEvent(name, payload)
        }
        bridge = FolioJsBridge(webView, app, this)

        val settings = webView.settings
        settings.javaScriptEnabled = true
        settings.domStorageEnabled = true
        settings.allowFileAccess = true
        settings.allowContentAccess = true
        settings.mixedContentMode = WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE
        settings.cacheMode = WebSettings.LOAD_DEFAULT
        settings.useWideViewPort = true
        settings.loadWithOverviewMode = true
        settings.builtInZoomControls = false
        settings.displayZoomControls = false
        settings.mediaPlaybackRequiresUserGesture = false
        settings.setSupportMultipleWindows(false)
        // Needed for large base64 page images in some WebView versions
        settings.loadsImagesAutomatically = true

        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)

        webView.addJavascriptInterface(bridge, "FolioAndroid")
        webView.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(
                webView: WebView?,
                filePathCallback: ValueCallback<Array<Uri>>?,
                fileChooserParams: FileChooserParams?
            ): Boolean {
                this@MainActivity.filePathCallback?.onReceiveValue(null)
                this@MainActivity.filePathCallback = filePathCallback
                return try {
                    val intent = fileChooserParams?.createIntent()
                        ?: Intent(Intent.ACTION_GET_CONTENT).apply {
                            addCategory(Intent.CATEGORY_OPENABLE)
                            type = "*/*"
                            putExtra(
                                Intent.EXTRA_MIME_TYPES,
                                arrayOf(
                                    "application/pdf",
                                    "application/epub+zip",
                                    "application/octet-stream"
                                )
                            )
                        }
                    htmlFileChooserLauncher.launch(intent)
                    true
                } catch (e: Exception) {
                    this@MainActivity.filePathCallback = null
                    filePathCallback?.onReceiveValue(null)
                    false
                }
            }
        }
        webView.webViewClient = object : WebViewClient() {
            override fun onPageStarted(view: WebView?, url: String?, favicon: android.graphics.Bitmap?) {
                view?.evaluateJavascript(FolioJsBridge.BOOTSTRAP_JS, null)
            }

            override fun onPageFinished(view: WebView?, url: String?) {
                view?.evaluateJavascript(FolioJsBridge.BOOTSTRAP_JS, null)
            }

            override fun shouldOverrideUrlLoading(view: WebView?, request: WebResourceRequest?): Boolean {
                val url = request?.url?.toString() ?: return false
                if (url.startsWith("file://") || url.startsWith("https://appassets.androidplatform.net")) {
                    return false
                }
                if (url.startsWith("http://") || url.startsWith("https://") || url.startsWith("mailto:")) {
                    try {
                        startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
                    } catch (_: Exception) {
                    }
                    return true
                }
                return false
            }
        }

        // Load embedded Folio UI
        webView.loadUrl("file:///android_asset/www/index.html")

        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                // Let the web UI handle Esc/back to shelf when possible
                webView.evaluateJavascript(
                    """
                    (function(){
                      try {
                        var reader = document.getElementById('view-reader');
                        if (reader && !reader.classList.contains('is-hidden')) {
                          var back = document.getElementById('btn-back');
                          if (back) { back.click(); return 'reader'; }
                        }
                      } catch(e) {}
                      return 'exit';
                    })();
                    """.trimIndent()
                ) { result ->
                    if (result == null || result.contains("exit")) {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                        isEnabled = true
                    }
                }
            }
        })

        handleIncomingIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleIncomingIntent(intent)
    }

    private fun handleIncomingIntent(intent: Intent?) {
        if (intent == null) return
        val uri = intent.data ?: return
        Thread {
            try {
                val path = importUriToBooks(uri)
                // Wait briefly for WebView bridge
                Thread.sleep(400)
                runOnUiThread {
                    webView.evaluateJavascript(
                        "window.go && window.go.main && window.go.main.App && window.go.main.App.OpenPath(${org.json.JSONObject.quote(path)}).then(function(){}).catch(function(){});",
                        null
                    )
                }
            } catch (_: Exception) {
            }
        }.start()
    }

    override fun pickBookFile(callbackId: String) {
        pendingPickCallbackId = callbackId
        openBookLauncher.launch(
            arrayOf(
                "application/pdf",
                "application/epub+zip",
                "application/octet-stream",
                "*/*"
            )
        )
    }

    override fun pickRemapFile(bookId: String, callbackId: String) {
        pendingPickCallbackId = callbackId
        pendingRemapBookId = bookId
        remapBookLauncher.launch(
            arrayOf(
                "application/pdf",
                "application/epub+zip",
                "application/octet-stream",
                "*/*"
            )
        )
    }

    /**
     * Copy a content/file URI into app-private books/ so PDF/EPUB engines have a stable path.
     */
    private fun importUriToBooks(uri: Uri): String {
        val books = FolioPaths.booksDir(this)
        val name = queryDisplayName(uri) ?: "book-${System.currentTimeMillis()}"
        val lower = name.lowercase()
        val ext = when {
            lower.endsWith(".epub") -> ".epub"
            lower.endsWith(".pdf") -> ".pdf"
            contentResolver.getType(uri)?.contains("epub") == true -> ".epub"
            contentResolver.getType(uri)?.contains("pdf") == true -> ".pdf"
            else -> {
                // sniff magic
                contentResolver.openInputStream(uri)?.use { input ->
                    val header = ByteArray(4)
                    val n = input.read(header)
                    when {
                        n >= 4 && header[0] == 0x50.toByte() && header[1] == 0x4B.toByte() -> ".epub"
                        n >= 4 && header[0] == 0x25.toByte() && header[1] == 0x50.toByte() -> ".pdf"
                        else -> ".pdf"
                    }
                } ?: ".pdf"
            }
        }
        val stem = sanitizeFilename(name.removeSuffix(".pdf").removeSuffix(".epub").removeSuffix(".PDF").removeSuffix(".EPUB"))
        val dest = uniquePath(File(books, stem + ext))
        contentResolver.openInputStream(uri)?.use { input ->
            FileOutputStream(dest).use { output -> input.copyTo(output) }
        } ?: throw IllegalStateException("Cannot read selected file")
        return dest.absolutePath
    }

    private fun queryDisplayName(uri: Uri): String? {
        return try {
            contentResolver.query(uri, arrayOf(android.provider.OpenableColumns.DISPLAY_NAME), null, null, null)
                ?.use { c ->
                    if (c.moveToFirst()) c.getString(0) else null
                }
        } catch (_: Exception) {
            null
        }
    }

    override fun onDestroy() {
        try {
            webView.destroy()
        } catch (_: Exception) {
        }
        super.onDestroy()
    }
}
