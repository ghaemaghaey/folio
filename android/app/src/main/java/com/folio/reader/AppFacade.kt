package com.folio.reader

import android.content.Context
import android.content.Intent
import android.net.Uri
import com.folio.reader.epub.EpubBook
import com.folio.reader.library.Book
import com.folio.reader.library.BookFormat
import com.folio.reader.library.BookStatus
import com.folio.reader.library.DEFAULT_OPDS_BASE_URL
import com.folio.reader.library.DocumentInfo
import com.folio.reader.library.EpubChapterDto
import com.folio.reader.library.FolioPaths
import com.folio.reader.library.LibraryStore
import com.folio.reader.library.OpdsIndex
import com.folio.reader.library.OpdsRecord
import com.folio.reader.library.PageImage
import com.folio.reader.library.Settings
import com.folio.reader.library.SettingsStore
import com.folio.reader.library.contentHash
import com.folio.reader.library.fingerprintFile
import com.folio.reader.library.inspectFile
import com.folio.reader.library.readingProgress
import com.folio.reader.library.readingState
import com.folio.reader.library.sanitizeFilename
import com.folio.reader.library.titleFromPath
import com.folio.reader.library.uniquePath
import com.folio.reader.opds.OpdsClient
import com.folio.reader.opds.OpdsEntry
import com.folio.reader.opds.OpdsFeed
import com.folio.reader.pdf.PdfRendererEngine
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

/**
 * Backend matching the desktop Wails [App] API surface used by frontend/dist/main.js.
 */
class AppFacade(
    private val context: Context,
    private val eventSink: (name: String, payload: JSONObject) -> Unit
) {
    private val lock = ReentrantLock()
    private val lib: LibraryStore = LibraryStore(FolioPaths.libraryFile(context))
    private val settings: SettingsStore = SettingsStore(FolioPaths.settingsFile(context))
    private val opdsIndex: OpdsIndex = OpdsIndex(FolioPaths.opdsIndexFile(context))
    private val pdf = PdfRendererEngine(FolioPaths.pdfCacheDir(context))

    private var openDoc: OpenDocument? = null
    private var epubBook: EpubBook? = null

    private data class OpenDocument(
        val id: String,
        val path: String,
        val title: String,
        val format: BookFormat,
        val pageCount: Int,
        var pageIndex: Int
    )

    fun appVersion(): String = "0.6.7"

    fun getLibrary(): JSONArray {
        val arr = JSONArray()
        lib.list().forEach { arr.put(it.toJson()) }
        return arr
    }

    fun openPath(path: String): JSONObject {
        val format = BookFormat.fromPath(path)
        val info = if (format == BookFormat.EPUB) openEpub(path, "") else openPdf(path, "")
        return info.toJson()
    }

    fun openBook(id: String): JSONObject {
        val book = lib.get(id) ?: throw IllegalArgumentException("book not found")
        val statusItem = lib.list().find { it.book.id == id }
        if (statusItem != null && statusItem.status != BookStatus.OK) {
            throw IllegalStateException(
                "book is ${statusItem.status.label.ifBlank { statusItem.status.value }} — remap the file first"
            )
        }
        val info = when (book.format) {
            BookFormat.EPUB -> openEpub(book.path, book.id)
            BookFormat.PDF -> openPdf(book.path, book.id)
        }
        return info.toJson()
    }

    fun removeFromLibrary(id: String) {
        lib.remove(id)
    }

    fun remapBook(id: String, newPath: String): JSONObject {
        lib.remap(id, newPath)
        return openBook(id)
    }

    fun saveProgress(pageIndex: Int, chapter: Int, subPage: Int, scroll: Double) {
        val id = lock.withLock {
            val doc = openDoc
            if (doc != null && doc.format == BookFormat.PDF && pageIndex >= 0) {
                doc.pageIndex = pageIndex
            }
            doc?.id
        } ?: return
        saveBookProgress(id, pageIndex, chapter, subPage, scroll)
    }

    fun saveBookProgress(bookID: String, pageIndex: Int, chapter: Int, subPage: Int, scroll: Double) {
        if (bookID.isBlank()) return
        lib.updateProgress(
            bookID,
            pageIndex.coerceAtLeast(0),
            chapter.coerceAtLeast(0),
            subPage.coerceAtLeast(0),
            scroll
        )
    }

    fun getBookProgress(bookID: String): JSONObject {
        val out = JSONObject()
            .put("page", 0).put("chapter", 0).put("subPage", 0).put("scroll", 0.0).put("id", bookID)
        if (bookID.isBlank()) return out
        val b = lib.get(bookID) ?: return out
        return out
            .put("page", b.lastPage)
            .put("chapter", b.lastChapter)
            .put("subPage", b.lastSubPage)
            .put("scroll", b.lastScroll)
    }

    fun getProgress(): JSONObject {
        val id = lock.withLock { openDoc?.id }.orEmpty()
        return getBookProgress(id)
    }

    fun renderPdfPage(pageIndex: Int, dpi: Int, setCurrent: Boolean): JSONObject {
        val (path, count) = lock.withLock {
            val doc = openDoc ?: throw IllegalStateException("no PDF open")
            if (doc.format != BookFormat.PDF) throw IllegalStateException("no PDF open")
            var idx = pageIndex
            if (idx < 0) idx = 0
            if (idx >= doc.pageCount) idx = doc.pageCount - 1
            if (setCurrent) doc.pageIndex = idx
            doc.path to doc.pageCount
        }
        val r = pdf.renderPage(path, pageIndex, if (dpi <= 0) 128 else dpi)
        return PageImage(r.dataURL, r.pageIndex, count, r.width, r.height).toJson()
    }

    fun renderCurrentPage(dpi: Int): JSONObject {
        val idx = lock.withLock { openDoc?.pageIndex ?: 0 }
        return renderPdfPage(idx, dpi, true)
    }

    fun goToPage(pageIndex: Int, dpi: Int): JSONObject? {
        val format = lock.withLock {
            val doc = openDoc ?: throw IllegalStateException("no document open")
            var idx = pageIndex
            if (idx < 0) idx = 0
            if (idx >= doc.pageCount) idx = doc.pageCount - 1
            doc.pageIndex = idx
            doc.format
        }
        if (format == BookFormat.EPUB) return null
        return renderCurrentPage(dpi)
    }

    fun nextPage(dpi: Int): JSONObject? {
        val format = lock.withLock {
            val doc = openDoc ?: throw IllegalStateException("no document open")
            if (doc.pageIndex + 1 < doc.pageCount) doc.pageIndex++
            doc.format
        }
        if (format == BookFormat.EPUB) return null
        return renderCurrentPage(dpi)
    }

    fun prevPage(dpi: Int): JSONObject? {
        val format = lock.withLock {
            val doc = openDoc ?: throw IllegalStateException("no document open")
            if (doc.pageIndex > 0) doc.pageIndex--
            doc.format
        }
        if (format == BookFormat.EPUB) return null
        return renderCurrentPage(dpi)
    }

    fun prefetchPdfPages(pages: List<Int>, dpi: Int) {
        val path = lock.withLock {
            val doc = openDoc
            if (doc == null || doc.format != BookFormat.PDF) return
            doc.path
        }
        val d = if (dpi <= 0) 128 else dpi
        for (p in pages) {
            if (p < 0) continue
            try {
                pdf.renderPage(path, p, d)
            } catch (_: Exception) {
            }
        }
    }

    fun getEpubChapter(index: Int): JSONObject {
        val (book, count) = lock.withLock {
            val b = epubBook ?: throw IllegalStateException("no EPUB open")
            val doc = openDoc ?: throw IllegalStateException("no EPUB open")
            if (doc.format != BookFormat.EPUB) throw IllegalStateException("no EPUB open")
            var idx = index
            if (idx < 0) idx = 0
            if (idx >= doc.pageCount) idx = doc.pageCount - 1
            doc.pageIndex = idx
            b to doc.pageCount
        }
        val ch = book.getChapter(index.coerceIn(0, count - 1))
        return EpubChapterDto(ch.index, ch.label, ch.html, count).toJson()
    }

    fun getAllEpubChapters(): JSONArray {
        val (book, count) = lock.withLock {
            val b = epubBook ?: throw IllegalStateException("no EPUB open")
            val doc = openDoc ?: throw IllegalStateException("no EPUB open")
            if (doc.format != BookFormat.EPUB) throw IllegalStateException("no EPUB open")
            b to doc.pageCount
        }
        val arr = JSONArray()
        for (i in 0 until count) {
            try {
                val ch = book.getChapter(i)
                arr.put(EpubChapterDto(ch.index, ch.label, ch.html, count).toJson())
            } catch (_: Exception) {
                arr.put(EpubChapterDto(i, "Chapter ${i + 1}", "<p></p>", count).toJson())
            }
        }
        return arr
    }

    fun getEpubChapterCount(): Int = lock.withLock {
        epubBook?.chapterCount() ?: throw IllegalStateException("no EPUB open")
    }

    fun getEpubToc(): JSONArray {
        val book = lock.withLock { epubBook } ?: throw IllegalStateException("no EPUB open")
        val arr = JSONArray()
        book.toc().forEach { t ->
            arr.put(JSONObject().put("index", t.index).put("label", t.label).put("href", t.href))
        }
        return arr
    }

    fun resolveEpubLink(href: String): JSONObject {
        val (book, from) = lock.withLock {
            val b = epubBook ?: throw IllegalStateException("no EPUB open")
            val doc = openDoc ?: throw IllegalStateException("no EPUB open")
            b to doc.pageIndex
        }
        val (idx, frag, ok) = book.resolveHref(from, href)
        return if (!ok) JSONObject().put("ok", false)
        else JSONObject().put("ok", true).put("index", idx).put("fragment", frag)
    }

    fun getDocument(): JSONObject? {
        val doc = lock.withLock { openDoc } ?: return null
        return DocumentInfo(
            id = doc.id,
            path = doc.path,
            title = doc.title,
            format = doc.format.value,
            pageCount = doc.pageCount,
            pageIndex = doc.pageIndex,
            status = "ok"
        ).toJson()
    }

    fun closeDocument() {
        lock.withLock {
            openDoc = null
            epubBook = null
        }
        pdf.closeDocument()
    }

    /**
     * Upload a local file path to Folio API. Emits folio:upload-progress events.
     */
    fun uploadLocalFile(
        apiBase: String,
        token: String,
        filePath: String,
        title: String,
        author: String
    ): JSONObject {
        var base = apiBase.trim().trimEnd('/')
        if (base.isEmpty()) throw IllegalArgumentException("api base is empty")
        if (token.isBlank()) throw IllegalArgumentException("not signed in")
        var path = filePath.trim()
        var useTitle = title
        if (path.isEmpty()) {
            lock.withLock {
                openDoc?.let {
                    path = it.path
                    if (useTitle.isBlank()) useTitle = it.title
                }
            }
        }
        if (path.isEmpty()) throw IllegalArgumentException("no local book path")
        val file = File(path)
        if (!file.exists()) throw IllegalArgumentException("file not found")
        if (useTitle.isBlank()) useTitle = titleFromPath(path)

        fun emit(percent: Double, done: Boolean, message: String) {
            eventSink(
                "folio:upload-progress",
                JSONObject()
                    .put("percent", percent)
                    .put("done", done)
                    .put("message", message)
            )
        }
        emit(0.0, false, "Preparing…")

        val boundary = "FolioBoundary${System.currentTimeMillis()}"
        val url = java.net.URL("$base/books/upload")
        val conn = (url.openConnection() as java.net.HttpURLConnection).apply {
            requestMethod = "POST"
            doOutput = true
            connectTimeout = 30_000
            readTimeout = 600_000
            setRequestProperty("Authorization", "Bearer $token")
            setRequestProperty("Content-Type", "multipart/form-data; boundary=$boundary")
            setRequestProperty("User-Agent", "Folio-Android/0.6.7")
        }

        val total = file.length().coerceAtLeast(1)
        conn.outputStream.use { out ->
            val writer = java.io.BufferedOutputStream(out)
            fun writeStr(s: String) = writer.write(s.toByteArray(Charsets.UTF_8))

            writeStr("--$boundary\r\n")
            writeStr("Content-Disposition: form-data; name=\"title\"\r\n\r\n")
            writeStr("$useTitle\r\n")
            if (author.isNotBlank()) {
                writeStr("--$boundary\r\n")
                writeStr("Content-Disposition: form-data; name=\"author\"\r\n\r\n")
                writeStr("$author\r\n")
            }
            writeStr("--$boundary\r\n")
            writeStr(
                "Content-Disposition: form-data; name=\"file\"; filename=\"${file.name}\"\r\n"
            )
            writeStr("Content-Type: application/octet-stream\r\n\r\n")
            writer.flush()

            file.inputStream().use { input ->
                val buf = ByteArray(256 * 1024)
                var written = 0L
                var lastPct = -1L
                while (true) {
                    val n = input.read(buf)
                    if (n <= 0) break
                    writer.write(buf, 0, n)
                    written += n
                    var pct = written * 100 / total
                    if (pct > 90) pct = 90
                    if (pct != lastPct) {
                        lastPct = pct
                        emit(pct.toDouble(), false, "Uploading…")
                    }
                }
            }
            writeStr("\r\n--$boundary--\r\n")
            writer.flush()
        }
        emit(92.0, false, "Sending…")
        val code = conn.responseCode
        val bodyStream = if (code in 200..299) conn.inputStream else conn.errorStream
        val raw = bodyStream?.bufferedReader()?.readText().orEmpty()
        conn.disconnect()
        if (code !in 200..299) {
            val msg = try {
                JSONObject(raw).optString("error").ifBlank { raw.ifBlank { "HTTP $code" } }
            } catch (_: Exception) {
                raw.ifBlank { "HTTP $code" }
            }
            emit(0.0, true, msg)
            throw IllegalStateException(msg)
        }
        emit(100.0, true, "Upload complete")
        return try {
            JSONObject(raw)
        } catch (_: Exception) {
            JSONObject().put("raw", raw)
        }
    }

    fun openExternalUrl(url: String) {
        val u = url.trim()
        val lower = u.lowercase()
        if (!(lower.startsWith("http://") || lower.startsWith("https://") || lower.startsWith("mailto:"))) {
            throw IllegalArgumentException("only http(s) and mailto links are allowed")
        }
        val intent = Intent(Intent.ACTION_VIEW, Uri.parse(u)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        context.startActivity(intent)
    }

    // --- OPDS ---

    fun getOpdsSettings(): JSONObject {
        val cfg = settings.get()
        return JSONObject()
            .put("baseURL", cfg.effectiveOpdsBaseURL())
            .put("username", cfg.opdsUsername)
            .put("password", cfg.opdsPassword)
            .put("booksDir", FolioPaths.booksDir(context).absolutePath)
    }

    fun saveOpdsSettings(baseURL: String, username: String, password: String): JSONObject {
        var base = baseURL.trim()
        if (base.isEmpty()) base = DEFAULT_OPDS_BASE_URL
        settings.update(Settings(base, username, password))
        return getOpdsSettings()
    }

    fun opdsOpenLibrary(): JSONObject {
        val feed = opdsClient().fetchBooksRoot()
        return feedToPage(feed)
    }

    fun opdsSearch(query: String): JSONObject {
        val feed = opdsClient().search(query)
        return feedToPage(feed)
    }

    fun opdsFetchPage(pageURL: String): JSONObject {
        if (pageURL.isBlank()) return opdsOpenLibrary()
        val feed = opdsClient().fetch(pageURL)
        return feedToPage(feed)
    }

    fun opdsDownload(
        opdsID: String,
        title: String,
        acquisitionHref: String,
        mimeType: String,
        coverURL: String = ""
    ): JSONObject {
        if (acquisitionHref.isBlank()) throw IllegalArgumentException("no acquisition link")
        val client = opdsClient()
        val booksDir = FolioPaths.booksDir(context)

        var ext = ".bin"
        var format = BookFormat.PDF
        val mt = mimeType.lowercase()
        when {
            mt.contains("epub") || acquisitionHref.lowercase().endsWith(".epub") -> {
                ext = ".epub"; format = BookFormat.EPUB
            }
            mt.contains("pdf") || acquisitionHref.lowercase().endsWith(".pdf") -> {
                ext = ".pdf"; format = BookFormat.PDF
            }
        }
        val stem = sanitizeFilename(title)
        var dest = uniquePath(File(booksDir, stem + ext))

        fun emit(percent: Double, done: Boolean, msg: String) {
            eventSink(
                "opds:download-progress",
                JSONObject()
                    .put("id", opdsID)
                    .put("title", title)
                    .put("percent", percent)
                    .put("done", done)
                    .put("message", msg)
                    .put("path", dest.absolutePath)
            )
        }
        emit(0.0, false, "Starting download…")

        val res = client.download(acquisitionHref)
        res.use { response ->
            val body = response.body ?: throw IllegalStateException("empty body")
            val total = body.contentLength()
            val tmp = File(dest.absolutePath + ".part")
            tmp.outputStream().use { out ->
                val input = body.byteStream()
                val buf = ByteArray(32 * 1024)
                var written = 0L
                var lastPct = -1
                while (true) {
                    val n = input.read(buf)
                    if (n <= 0) break
                    out.write(buf, 0, n)
                    written += n
                    if (total > 0) {
                        val pct = ((written * 100) / total).toInt()
                        if (pct != lastPct) {
                            lastPct = pct
                            emit(pct.toDouble(), false, "Downloading… $pct%")
                        }
                    }
                }
            }

            val fp = fingerprintFile(tmp)
            val chash = contentHash(tmp)

            lib.findByFingerprint(fp)?.let { existing ->
                if (File(existing.path).exists()) {
                    tmp.delete()
                    dest = File(existing.path)
                    if (opdsID.isNotBlank()) {
                        opdsIndex.put(
                            OpdsRecord(
                                opdsId = opdsID,
                                title = title,
                                localPath = existing.path,
                                localBookId = existing.id,
                                fingerprint = existing.fingerprint,
                                contentHash = chash,
                                size = existing.fileSize,
                                format = existing.format.value
                            )
                        )
                    }
                    emit(100.0, true, "Already in your shelf")
                    val dto = enrichEntry(
                        OpdsEntry(
                            id = opdsID,
                            title = title,
                            acquisitions = listOf(
                                com.folio.reader.opds.Acquisition(acquisitionHref, mimeType)
                            )
                        )
                    )
                    dto.put("localBookId", existing.id)
                    dto.put("localPath", existing.path)
                    val state = readingState(existing)
                    dto.put("state", if (state == "downloaded") "downloaded" else state)
                    if (state == "in_progress") {
                        val prog = readingProgress(existing)
                        dto.put("progress", prog)
                        dto.put("progressLabel", "${(prog * 100 + 0.5).toInt()}%")
                    } else if (state == "read") {
                        dto.put("progressLabel", "Read")
                    } else {
                        dto.put("progressLabel", "Downloaded")
                    }
                    return JSONObject()
                        .put("book", dto)
                        .put("localId", existing.id)
                        .put("path", existing.path)
                        .put("skipped", true)
                }
            }

            if (dest.exists()) dest.delete()
            if (!tmp.renameTo(dest)) {
                tmp.copyTo(dest, overwrite = true)
                tmp.delete()
            }

            val meta = inspectFile(dest)
            var book = Book(
                path = dest.absolutePath,
                title = title.ifBlank { stem },
                format = format,
                coverDataURL = coverURL.ifEmpty { null },
                fileSize = meta.size,
                modTimeUnix = meta.modTimeUnix,
                fingerprint = meta.fingerprint
            )
            book = lib.upsert(book)
            if (opdsID.isNotBlank()) {
                opdsIndex.put(
                    OpdsRecord(
                        opdsId = opdsID,
                        title = book.title,
                        localPath = dest.absolutePath,
                        localBookId = book.id,
                        fingerprint = meta.fingerprint,
                        contentHash = chash,
                        size = meta.size,
                        format = format.value
                    )
                )
            }
            emit(100.0, true, "Download complete")
            val dto = JSONObject()
                .put("id", opdsID)
                .put("title", book.title)
                .put("state", "downloaded")
                .put("progressLabel", "Downloaded")
                .put("localBookId", book.id)
                .put("localPath", dest.absolutePath)
                .put(
                    "acquisitions",
                    JSONArray().put(
                        JSONObject()
                            .put("href", acquisitionHref)
                            .put("type", mimeType)
                            .put("format", format.value)
                    )
                )
            return JSONObject()
                .put("book", dto)
                .put("localId", book.id)
                .put("path", dest.absolutePath)
                .put("skipped", false)
        }
    }

    private fun opdsClient(): OpdsClient {
        val cfg = settings.get()
        return OpdsClient(cfg.effectiveOpdsBaseURL(), cfg.opdsUsername, cfg.opdsPassword)
    }

    private fun feedToPage(feed: OpdsFeed): JSONObject {
        val books = JSONArray()
        feed.entries.forEach { books.put(enrichEntry(it)) }
        return JSONObject()
            .put("title", feed.title)
            .put("selfURL", feed.selfURL)
            .put("nextURL", feed.nextURL)
            .put("books", books)
    }

    private fun enrichEntry(e: OpdsEntry): JSONObject {
        val dto = JSONObject()
            .put("id", e.id)
            .put("title", e.title)
            .put("authors", JSONArray(e.authors))
            .put("summary", e.summary)
            .put("coverURL", e.thumbnailURL.ifBlank { e.coverURL })
            .put("isNavigation", e.isNavigation)
            .put("navURL", e.navURL)
            .put("state", "not_downloaded")
            .put("progress", 0)
        val acqArr = JSONArray()
        e.acquisitions.forEach { a ->
            acqArr.put(
                JSONObject()
                    .put("href", a.href)
                    .put("type", a.type)
                    .put("length", a.length)
                    .put("format", a.formatLabel())
            )
        }
        dto.put("acquisitions", acqArr)
        if (e.isNavigation || e.acquisitions.isEmpty()) return dto

        val local = resolveLocalForOpds(e) ?: return dto
        dto.put("localBookId", local.id)
        dto.put("localPath", local.path)
        val prog = readingProgress(local)
        dto.put("progress", prog)
        when (readingState(local)) {
            "read" -> {
                dto.put("state", "read")
                dto.put("progressLabel", "Read")
            }
            "in_progress" -> {
                dto.put("state", "in_progress")
                dto.put("progressLabel", "${(prog * 100 + 0.5).toInt()}%")
            }
            else -> {
                dto.put("state", "downloaded")
                dto.put("progressLabel", "Downloaded")
            }
        }
        return dto
    }

    private fun resolveLocalForOpds(e: OpdsEntry): Book? {
        if (e.id.isNotBlank()) {
            opdsIndex.get(e.id)?.let { rec ->
                if (rec.localBookId.isNotBlank()) {
                    lib.get(rec.localBookId)?.let { b ->
                        if (File(b.path).exists()) return b
                    }
                }
                if (rec.localPath.isNotBlank()) {
                    lib.findByPath(rec.localPath)?.let { b ->
                        if (File(b.path).exists()) return b
                    }
                    if (File(rec.localPath).exists()) {
                        val meta = inspectFile(File(rec.localPath))
                        return Book(
                            id = rec.localBookId,
                            path = rec.localPath,
                            title = rec.title.ifBlank { e.title },
                            format = BookFormat.fromPath(rec.localPath),
                            fileSize = meta.size,
                            fingerprint = meta.fingerprint
                        )
                    }
                }
                if (rec.fingerprint.isNotBlank()) {
                    lib.findByFingerprint(rec.fingerprint)?.let { b ->
                        if (File(b.path).exists()) return b
                    }
                }
            }
        }
        val acq = e.preferredAcquisition()
        if (acq != null && acq.length > 0) {
            for (item in lib.list()) {
                if (item.status != BookStatus.OK) continue
                if (item.book.fileSize == acq.length &&
                    item.book.title.trim().equals(e.title.trim(), ignoreCase = true)
                ) {
                    return item.book
                }
            }
        }
        return null
    }

    private fun openPdf(path: String, existingId: String): DocumentInfo {
        val count = pdf.open(path)
        val meta = inspectFile(File(path))
        var book = Book(
            id = existingId,
            path = path,
            title = titleFromPath(path),
            format = BookFormat.PDF,
            pageCount = count,
            fileSize = meta.size,
            modTimeUnix = meta.modTimeUnix,
            fingerprint = meta.fingerprint
        )
        var lastPage = 0
        var lastScroll = 0.0
        if (existingId.isNotBlank()) {
            lib.get(existingId)?.let { prev ->
                lastPage = prev.lastPage
                lastScroll = prev.lastScroll
                if (prev.title.isNotBlank()) book = book.copy(title = prev.title)
            }
        } else {
            lib.findByPath(path)?.let {
                lastPage = it.lastPage
                lastScroll = it.lastScroll
                book = book.copy(id = it.id, title = it.title)
            }
        }
        if (lastPage >= count) lastPage = count - 1
        if (lastPage < 0) lastPage = 0
        book = book.copy(pageCount = count)
        val saved = lib.upsert(book)
        lastPage = saved.lastPage.coerceIn(0, (count - 1).coerceAtLeast(0))
        lastScroll = saved.lastScroll

        lock.withLock {
            epubBook = null
            openDoc = OpenDocument(
                id = saved.id,
                path = path,
                title = saved.title,
                format = BookFormat.PDF,
                pageCount = count,
                pageIndex = lastPage
            )
        }

        // Cover thumbnail in background
        if (saved.coverDataURL.isNullOrBlank()) {
            Thread {
                try {
                    val r = pdf.renderPage(path, 0, 48)
                    lib.setCover(saved.id, r.dataURL)
                } catch (_: Exception) {
                }
            }.start()
        }

        return DocumentInfo(
            id = saved.id,
            path = path,
            title = saved.title,
            format = BookFormat.PDF.value,
            pageCount = count,
            pageIndex = lastPage,
            lastScroll = lastScroll,
            fingerprint = saved.fingerprint,
            status = "ok"
        )
    }

    private fun openEpub(path: String, existingId: String): DocumentInfo {
        val bookEpub = EpubBook.open(path)
        val meta = inspectFile(File(path))
        var title = bookEpub.title
        if (title.isBlank() || title == "Untitled") title = titleFromPath(path)
        val chapterCount = bookEpub.chapterCount()
        if (chapterCount < 1) throw IllegalArgumentException("EPUB has no chapters")

        var libBook = Book(
            id = existingId,
            path = path,
            title = title,
            format = BookFormat.EPUB,
            pageCount = chapterCount,
            fileSize = meta.size,
            modTimeUnix = meta.modTimeUnix,
            fingerprint = meta.fingerprint
        )

        // Extract EPUB cover image if available
        val coverUrl = bookEpub.coverDataURL()
        if (coverUrl.isNotEmpty()) {
            libBook = libBook.copy(coverDataURL = coverUrl)
        }

        var lastPage = 0
        var lastChapter = 0
        var lastSubPage = 0
        var lastScroll = 0.0
        if (existingId.isNotBlank()) {
            lib.get(existingId)?.let { prev ->
                lastPage = prev.lastPage
                lastChapter = prev.lastChapter
                lastSubPage = prev.lastSubPage
                lastScroll = prev.lastScroll
            }
        } else {
            lib.findByPath(path)?.let {
                lastPage = it.lastPage
                lastChapter = it.lastChapter
                lastSubPage = it.lastSubPage
                lastScroll = it.lastScroll
                libBook = libBook.copy(id = it.id)
            }
        }
        if (lastChapter >= chapterCount) lastChapter = chapterCount - 1
        if (lastChapter < 0) lastChapter = 0
        if (lastSubPage < 0) lastSubPage = 0
        if (lastPage < 0) lastPage = 0
        val saved = lib.upsert(libBook)
        lastPage = saved.lastPage
        lastChapter = saved.lastChapter
        lastSubPage = saved.lastSubPage
        lastScroll = saved.lastScroll

        lock.withLock {
            epubBook = bookEpub
            openDoc = OpenDocument(
                id = saved.id,
                path = path,
                title = saved.title,
                format = BookFormat.EPUB,
                pageCount = chapterCount,
                pageIndex = lastPage
            )
        }

        return DocumentInfo(
            id = saved.id,
            path = path,
            title = saved.title,
            format = BookFormat.EPUB.value,
            pageCount = chapterCount,
            pageIndex = lastPage,
            lastChapter = lastChapter,
            lastSubPage = lastSubPage,
            lastScroll = lastScroll,
            fingerprint = saved.fingerprint,
            status = "ok"
        )
    }

    private fun Book.copy(
        id: String = this.id,
        path: String = this.path,
        title: String = this.title,
        format: BookFormat = this.format,
        pageCount: Int = this.pageCount,
        lastPage: Int = this.lastPage,
        lastChapter: Int = this.lastChapter,
        lastSubPage: Int = this.lastSubPage,
        lastScroll: Double = this.lastScroll,
        fileSize: Long = this.fileSize,
        modTimeUnix: Long = this.modTimeUnix,
        fingerprint: String = this.fingerprint,
        addedAtUnix: Long = this.addedAtUnix,
        openedAtUnix: Long = this.openedAtUnix,
        coverDataURL: String? = this.coverDataURL
    ) = Book(
        id, path, title, format, pageCount, lastPage, lastChapter, lastSubPage,
        lastScroll, fileSize, modTimeUnix, fingerprint, addedAtUnix, openedAtUnix, coverDataURL
    )
}
