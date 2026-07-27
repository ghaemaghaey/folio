package com.folio.reader.library

import org.json.JSONArray
import org.json.JSONObject

enum class BookFormat(val value: String) {
    PDF("pdf"),
    EPUB("epub");

    companion object {
        fun fromPath(path: String): BookFormat {
            return if (path.lowercase().endsWith(".epub")) EPUB else PDF
        }

        fun from(value: String?): BookFormat {
            return if (value.equals("epub", true)) EPUB else PDF
        }
    }
}

enum class BookStatus(val value: String, val label: String) {
    OK("ok", ""),
    MISSING("missing", "Removed"),
    REPLACED("replaced", "Replaced")
}

data class Book(
    var id: String = "",
    var path: String = "",
    var title: String = "",
    var format: BookFormat = BookFormat.PDF,
    var pageCount: Int = 0,
    var lastPage: Int = 0,
    var lastChapter: Int = 0,
    var lastSubPage: Int = 0,
    var lastScroll: Double = 0.0,
    var fileSize: Long = 0,
    var modTimeUnix: Long = 0,
    var fingerprint: String = "",
    var addedAtUnix: Long = 0,
    var openedAtUnix: Long = 0,
    var coverDataURL: String? = null
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("id", id)
        put("path", path)
        put("title", title)
        put("format", format.value)
        put("pageCount", pageCount)
        put("lastPage", lastPage)
        put("lastChapter", lastChapter)
        put("lastSubPage", lastSubPage)
        put("lastScroll", lastScroll)
        put("fileSize", fileSize)
        put("modTimeUnix", modTimeUnix)
        put("fingerprint", fingerprint)
        put("addedAtUnix", addedAtUnix)
        put("openedAtUnix", openedAtUnix)
        if (!coverDataURL.isNullOrBlank()) put("coverDataURL", coverDataURL)
    }

    companion object {
        fun fromJson(o: JSONObject): Book = Book(
            id = o.optString("id"),
            path = o.optString("path"),
            title = o.optString("title"),
            format = BookFormat.from(o.optString("format")),
            pageCount = o.optInt("pageCount"),
            lastPage = o.optInt("lastPage"),
            lastChapter = o.optInt("lastChapter"),
            lastSubPage = o.optInt("lastSubPage"),
            lastScroll = o.optDouble("lastScroll", 0.0),
            fileSize = o.optLong("fileSize"),
            modTimeUnix = o.optLong("modTimeUnix"),
            fingerprint = o.optString("fingerprint"),
            addedAtUnix = o.optLong("addedAtUnix"),
            openedAtUnix = o.optLong("openedAtUnix"),
            coverDataURL = o.optString("coverDataURL").ifBlank { null }
        )
    }
}

data class ShelfItem(
    val book: Book,
    val status: BookStatus
) {
    fun toJson(): JSONObject = book.toJson().apply {
        put("status", status.value)
        put("statusLabel", status.label)
        // Frontend may look at both nested and flat shapes
        put("addedAt", book.addedAtUnix)
        put("openedAt", book.openedAtUnix)
    }
}

data class DocumentInfo(
    val id: String,
    val path: String,
    val title: String,
    val format: String,
    val pageCount: Int,
    val pageIndex: Int = 0,
    val lastChapter: Int = 0,
    val lastSubPage: Int = 0,
    val lastScroll: Double = 0.0,
    val status: String = "ok"
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("id", id)
        put("path", path)
        put("title", title)
        put("format", format)
        put("pageCount", pageCount)
        put("pageIndex", pageIndex)
        put("lastChapter", lastChapter)
        put("lastSubPage", lastSubPage)
        put("lastScroll", lastScroll)
        put("status", status)
    }
}

data class PageImage(
    val dataURL: String,
    val pageIndex: Int,
    val pageCount: Int,
    val width: Int,
    val height: Int
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("dataURL", dataURL)
        put("pageIndex", pageIndex)
        put("pageCount", pageCount)
        put("width", width)
        put("height", height)
    }
}

data class EpubChapterDto(
    val index: Int,
    val label: String,
    val html: String,
    val chapterCount: Int
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("index", index)
        put("label", label)
        put("html", html)
        put("chapterCount", chapterCount)
    }
}

fun JSONArray.toStringList(): List<String> {
    val out = ArrayList<String>(length())
    for (i in 0 until length()) out.add(optString(i))
    return out
}

fun readingProgress(b: Book): Double {
    if (b.lastScroll > 0.001) return b.lastScroll.coerceIn(0.0, 1.0)
    if (b.pageCount > 1 && b.lastPage > 0) {
        return (b.lastPage.toDouble() / (b.pageCount - 1)).coerceIn(0.0, 1.0)
    }
    if (b.pageCount == 1 && b.lastPage > 0) return 1.0
    return 0.0
}

fun readingState(b: Book): String {
    val p = readingProgress(b)
    return when {
        p >= 0.97 -> "read"
        p > 0.01 || b.lastPage > 0 || b.lastChapter > 0 -> "in_progress"
        else -> "downloaded"
    }
}
