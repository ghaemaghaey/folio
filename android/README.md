# Folio for Android

Android package of the Folio monorepo. See the [root README](../README.md).

This app reuses the desktop frontend (HTML/CSS/JS) in a WebView, with a Kotlin backend for PDF, EPUB, library, and OPDS.

## Run

Open the monorepo `android/` folder in Android Studio, or:

```bash
cd android
./gradlew :app:assembleDebug
```

APK: `app/build/outputs/apk/debug/app-debug.apk`

## Notes

- Min SDK 26, compile SDK 35
- Opened documents are copied into app-private `books/`
- Default OPDS: `https://calibre.ghaemghh.ir`
