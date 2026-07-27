package com.folio.reader.pdf

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.pdf.PdfRenderer
import android.os.ParcelFileDescriptor
import android.util.Base64
import android.util.LruCache
import java.io.ByteArrayOutputStream
import java.io.File
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock
import kotlin.math.max
import kotlin.math.roundToInt

/**
 * PDF page renderer using Android's [PdfRenderer].
 * Zoom is applied by the frontend via CSS; we render at a fixed base DPI-like scale.
 */
class PdfRendererEngine(private val diskCacheRoot: File?) {
    private val lock = ReentrantLock()
    private var pfd: ParcelFileDescriptor? = null
    private var renderer: PdfRenderer? = null
    private var openPath: String? = null
    private var pageCount: Int = 0

    private val memCache = object : LruCache<String, CacheEntry>(32) {
        override fun sizeOf(key: String, value: CacheEntry): Int = 1
    }

    data class CacheEntry(
        val dataURL: String,
        val width: Int,
        val height: Int
    )

    data class RenderResult(
        val dataURL: String,
        val width: Int,
        val height: Int,
        val pageCount: Int,
        val pageIndex: Int
    )

    fun open(path: String): Int = lock.withLock {
        if (openPath == path && renderer != null) return pageCount
        closeLocked()
        val file = File(path)
        if (!file.exists()) throw IllegalArgumentException("PDF not found: $path")
        val fd = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
        val r = PdfRenderer(fd)
        pfd = fd
        renderer = r
        openPath = path
        pageCount = r.pageCount
        return pageCount
    }

    fun getPageCount(): Int = lock.withLock { pageCount }

    fun renderPage(path: String, pageIndex: Int, dpi: Int): RenderResult = lock.withLock {
        if (openPath != path || renderer == null) {
            openLocked(path)
        }
        val r = renderer ?: throw IllegalStateException("PDF engine not ready")
        val count = pageCount
        var idx = pageIndex
        if (idx < 0) idx = 0
        if (idx >= count) idx = count - 1

        val scaleDpi = if (dpi <= 0) 128 else dpi
        val cacheKey = "$path|$idx|$scaleDpi"
        memCache.get(cacheKey)?.let {
            return RenderResult(it.dataURL, it.width, it.height, count, idx)
        }

        diskRead(path, idx, scaleDpi)?.let {
            memCache.put(cacheKey, it)
            return RenderResult(it.dataURL, it.width, it.height, count, idx)
        }

        val page = r.openPage(idx)
        try {
            // PdfRenderer uses 1/72 inch points; scale so width ~ dpi/72 * pageWidth
            val scale = scaleDpi / 72f
            val w = max(1, (page.width * scale).roundToInt())
            val h = max(1, (page.height * scale).roundToInt())
            val bitmap = Bitmap.createBitmap(w, h, Bitmap.Config.ARGB_8888)
            val canvas = Canvas(bitmap)
            canvas.drawColor(Color.WHITE)
            page.render(bitmap, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
            val dataURL = bitmapToJpegDataUrl(bitmap, 88)
            bitmap.recycle()
            val entry = CacheEntry(dataURL, w, h)
            memCache.put(cacheKey, entry)
            diskWrite(path, idx, scaleDpi, entry)
            return RenderResult(dataURL, w, h, count, idx)
        } finally {
            page.close()
        }
    }

    fun closeDocument() = lock.withLock {
        closeLocked()
        memCache.evictAll()
    }

    fun close() = lock.withLock {
        closeLocked()
        memCache.evictAll()
    }

    private fun openLocked(path: String) {
        closeLocked()
        val file = File(path)
        if (!file.exists()) throw IllegalArgumentException("PDF not found: $path")
        val fd = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
        val r = PdfRenderer(fd)
        pfd = fd
        renderer = r
        openPath = path
        pageCount = r.pageCount
    }

    private fun closeLocked() {
        try {
            renderer?.close()
        } catch (_: Exception) {
        }
        try {
            pfd?.close()
        } catch (_: Exception) {
        }
        renderer = null
        pfd = null
        openPath = null
        pageCount = 0
    }

    private fun diskDir(path: String): File? {
        val root = diskCacheRoot ?: return null
        val hash = path.hashCode().toUInt().toString(16)
        val dir = File(root, hash)
        if (!dir.exists()) dir.mkdirs()
        return dir
    }

    private fun diskFile(path: String, page: Int, dpi: Int): File? {
        val dir = diskDir(path) ?: return null
        return File(dir, "p${page}_d${dpi}.jpg")
    }

    private fun diskRead(path: String, page: Int, dpi: Int): CacheEntry? {
        val f = diskFile(path, page, dpi) ?: return null
        if (!f.exists()) return null
        return try {
            val bytes = f.readBytes()
            val b64 = Base64.encodeToString(bytes, Base64.NO_WRAP)
            // decode size via BitmapFactory would be heavier; store sidecar optional — use 0 dims if unknown
            CacheEntry("data:image/jpeg;base64,$b64", 0, 0)
        } catch (_: Exception) {
            null
        }
    }

    private fun diskWrite(path: String, page: Int, dpi: Int, entry: CacheEntry) {
        val f = diskFile(path, page, dpi) ?: return
        try {
            val raw = entry.dataURL.substringAfter("base64,", missingDelimiterValue = "")
            if (raw.isEmpty()) return
            f.writeBytes(Base64.decode(raw, Base64.DEFAULT))
        } catch (_: Exception) {
        }
    }

    companion object {
        fun bitmapToJpegDataUrl(bitmap: Bitmap, quality: Int): String {
            val baos = ByteArrayOutputStream()
            bitmap.compress(Bitmap.CompressFormat.JPEG, quality, baos)
            val b64 = Base64.encodeToString(baos.toByteArray(), Base64.NO_WRAP)
            return "data:image/jpeg;base64,$b64"
        }
    }
}
