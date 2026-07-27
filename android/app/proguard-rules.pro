# Folio WebView bridge — keep JS interface names
-keepclassmembers class com.folio.reader.bridge.FolioJsBridge {
    @android.webkit.JavascriptInterface <methods>;
}
