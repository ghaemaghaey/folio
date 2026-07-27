package com.folio.reader.library

import android.content.Context
import java.io.File
import java.security.MessageDigest
import java.util.Locale

object FolioPaths {
    fun rootDir(context: Context): File {
        val dir = File(context.filesDir, ".folio")
        if (!dir.exists()) dir.mkdirs()
        return dir
    }

    fun libraryFile(context: Context): File = File(rootDir(context), "library.json")

    fun settingsFile(context: Context): File = File(rootDir(context), "settings.json")

    fun opdsIndexFile(context: Context): File = File(rootDir(context), "opds-index.json")

    fun booksDir(context: Context): File {
        val dir = File(context.filesDir, "books")
        if (!dir.exists()) dir.mkdirs()
        return dir
    }

    fun pdfCacheDir(context: Context): File {
        val dir = File(rootDir(context), "cache/pdf")
        if (!dir.exists()) dir.mkdirs()
        return dir
    }
}

fun sanitizeFilename(title: String): String {
    var t = title.trim().ifEmpty { "book" }
    val b = StringBuilder()
    for (r in t) {
        when {
            r.isLetterOrDigit() && r.code < 128 -> b.append(r)
            r == ' ' || r == '-' || r == '_' || r == '.' -> b.append('-')
            r.code > 127 -> b.append(r)
        }
    }
    var out = b.toString().trim('-', '.')
    while (out.contains("--")) out = out.replace("--", "-")
    if (out.isEmpty()) out = "book"
    if (out.length > 120) out = out.take(120)
    return out
}

fun uniquePath(dest: File): File {
    if (!dest.exists()) return dest
    val parent = dest.parentFile ?: return dest
    val name = dest.nameWithoutExtension
    val ext = dest.extension.let { if (it.isEmpty()) "" else ".$it" }
    var i = 2
    while (true) {
        val candidate = File(parent, "$name-$i$ext")
        if (!candidate.exists()) return candidate
        i++
    }
}

fun titleFromPath(path: String): String {
    val base = File(path).name
    val dot = base.lastIndexOf('.')
    return if (dot > 0) base.substring(0, dot) else base
}

fun fingerprintFile(file: File): String {
    val md = MessageDigest.getInstance("SHA-256")
    file.inputStream().use { input ->
        val buf = ByteArray(64 * 1024)
        val n = input.read(buf)
        if (n > 0) md.update(buf, 0, n)
    }
    return md.digest().joinToString("") { "%02x".format(it) }
}

fun contentHash(file: File): String {
    val md = MessageDigest.getInstance("SHA-256")
    file.inputStream().use { input ->
        val buf = ByteArray(32 * 1024)
        while (true) {
            val n = input.read(buf)
            if (n <= 0) break
            md.update(buf, 0, n)
        }
    }
    return md.digest().joinToString("") { "%02x".format(it) }
}

data class FileMeta(
    val size: Long,
    val modTimeUnix: Long,
    val fingerprint: String
)

fun inspectFile(file: File): FileMeta {
    return FileMeta(
        size = file.length(),
        modTimeUnix = file.lastModified() / 1000,
        fingerprint = fingerprintFile(file)
    )
}

fun equalPath(a: String, b: String): Boolean {
    return a.replace('\\', '/').equals(b.replace('\\', '/'), ignoreCase = true)
}
