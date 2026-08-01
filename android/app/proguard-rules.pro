# Folio WebView bridge — keep JS interface names
-keepclassmembers class com.folio.reader.bridge.FolioJsBridge {
    @android.webkit.JavascriptInterface <methods>;
}

# Keep the bridge class itself (instantiated by MainActivity, referenced from JS)
-keep class com.folio.reader.bridge.FolioJsBridge { <init>(...); }

# Keep enum valueOf / values (used in JSON serialization and parsing)
-keepclassmembers enum com.folio.reader.library.* {
    *;
}

# OkHttp uses reflection for platform detection — keep its platform classes
-dontwarn okhttp3.internal.platform.**
-keep class okhttp3.internal.platform.** { *; }
