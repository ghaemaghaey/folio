package com.folio.reader.library

import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.UUID
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

class LibraryStore(private val path: File) {
    private val lock = ReentrantLock()
    private var books: MutableList<Book> = mutableListOf()

    init {
        load()
    }

    private fun load() {
        lock.withLock {
            if (!path.exists()) {
                books = mutableListOf()
                saveLocked()
                return
            }
            val text = path.readText()
            if (text.isBlank()) {
                books = mutableListOf()
                return
            }
            val root = JSONObject(text)
            val arr = root.optJSONArray("books") ?: JSONArray()
            val list = mutableListOf<Book>()
            for (i in 0 until arr.length()) {
                list.add(Book.fromJson(arr.getJSONObject(i)))
            }
            books = list
        }
    }

    private fun saveLocked() {
        path.parentFile?.mkdirs()
        val arr = JSONArray()
        books.forEach { arr.put(it.toJson()) }
        val root = JSONObject().put("version", 1).put("books", arr)
        val tmp = File(path.absolutePath + ".tmp")
        tmp.writeText(root.toString(2))
        if (path.exists()) path.delete()
        tmp.renameTo(path)
    }

    fun list(): List<ShelfItem> = lock.withLock {
        books.sortedByDescending { it.openedAtUnix }.map { b ->
            ShelfItem(b.copy(), probeStatus(b))
        }
    }

    fun get(id: String): Book? = lock.withLock {
        books.find { it.id == id }?.copy()
    }

    fun findByPath(p: String): Book? = lock.withLock {
        books.find { equalPath(it.path, p) }?.copy()
    }

    fun findByFingerprint(fp: String): Book? = lock.withLock {
        if (fp.isBlank()) return@withLock null
        books.find { it.fingerprint == fp }?.copy()
    }

    fun upsert(book: Book): Book = lock.withLock {
        val now = System.currentTimeMillis() / 1000
        var b = book
        if (b.id.isBlank()) b = b.copy(id = newId())
        if (b.addedAtUnix == 0L) b = b.copy(addedAtUnix = now)
        b = b.copy(openedAtUnix = now)

        val idx = books.indexOfFirst { it.id == b.id || equalPath(it.path, b.path) }
        if (idx >= 0) {
            val prev = books[idx]
            b = b.copy(
                id = prev.id,
                addedAtUnix = if (b.addedAtUnix == 0L) prev.addedAtUnix else b.addedAtUnix,
                coverDataURL = b.coverDataURL ?: prev.coverDataURL,
                lastPage = prev.lastPage,
                lastChapter = prev.lastChapter,
                lastSubPage = prev.lastSubPage,
                lastScroll = prev.lastScroll
            )
            books[idx] = b
        } else {
            books.add(b)
        }
        saveLocked()
        b.copy()
    }

    fun updateProgress(
        id: String,
        lastPage: Int,
        lastChapter: Int,
        lastSubPage: Int,
        lastScroll: Double
    ) {
        lock.withLock {
            val idx = books.indexOfFirst { it.id == id }
            if (idx < 0) throw IllegalArgumentException("book not found")
            val now = System.currentTimeMillis() / 1000
            books[idx] = books[idx].copy(
                lastPage = lastPage.coerceAtLeast(0),
                lastChapter = lastChapter.coerceAtLeast(0),
                lastSubPage = lastSubPage.coerceAtLeast(0),
                lastScroll = lastScroll,
                openedAtUnix = now
            )
            saveLocked()
        }
    }

    fun remap(id: String, newPath: String): Book = lock.withLock {
        val file = File(newPath)
        val meta = inspectFile(file)
        val idx = books.indexOfFirst { it.id == id }
        if (idx < 0) throw IllegalArgumentException("book not found")
        val now = System.currentTimeMillis() / 1000
        val b = books[idx].copy(
            path = newPath,
            fileSize = meta.size,
            modTimeUnix = meta.modTimeUnix,
            fingerprint = meta.fingerprint,
            title = titleFromPath(newPath),
            format = BookFormat.fromPath(newPath),
            openedAtUnix = now
        )
        books[idx] = b
        saveLocked()
        b.copy()
    }

    fun remove(id: String) {
        lock.withLock {
            books.removeAll { it.id == id }
            saveLocked()
        }
    }

    fun setCover(id: String, coverDataURL: String) {
        lock.withLock {
            val idx = books.indexOfFirst { it.id == id }
            if (idx < 0) return
            if (!books[idx].coverDataURL.isNullOrBlank()) return
            books[idx] = books[idx].copy(coverDataURL = coverDataURL)
            saveLocked()
        }
    }

    private fun probeStatus(b: Book): BookStatus {
        val f = File(b.path)
        if (!f.exists()) return BookStatus.MISSING
        val size = f.length()
        // Fast path: same size → skip 64KiB rehash (startup-critical).
        if (b.fileSize > 0 && size == b.fileSize) return BookStatus.OK
        if (b.fileSize > 0 && size != b.fileSize) {
            if (b.fingerprint.isNotBlank()) {
                return try {
                    if (fingerprintFile(f) != b.fingerprint) BookStatus.REPLACED else BookStatus.OK
                } catch (_: Exception) {
                    BookStatus.MISSING
                }
            }
            return BookStatus.REPLACED
        }
        if (b.fingerprint.isNotBlank()) {
            return try {
                if (fingerprintFile(f) != b.fingerprint) BookStatus.REPLACED else BookStatus.OK
            } catch (_: Exception) {
                BookStatus.MISSING
            }
        }
        return BookStatus.OK
    }

    private fun newId(): String = UUID.randomUUID().toString().replace("-", "").take(16)

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
