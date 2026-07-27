package com.folio.reader.library

import org.json.JSONObject
import java.io.File
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

const val DEFAULT_OPDS_BASE_URL = "https://calibre.ghaemghh.ir"

data class Settings(
    var opdsBaseURL: String = DEFAULT_OPDS_BASE_URL,
    var opdsUsername: String = "",
    var opdsPassword: String = ""
) {
    fun effectiveOpdsBaseURL(): String {
        val u = opdsBaseURL.trim().trimEnd('/')
        return u.ifEmpty { DEFAULT_OPDS_BASE_URL }
    }
}

class SettingsStore(private val path: File) {
    private val lock = ReentrantLock()
    private var data = Settings()

    init {
        load()
    }

    private fun load() {
        lock.withLock {
            if (!path.exists()) {
                saveLocked()
                return
            }
            val text = path.readText()
            if (text.isBlank()) return
            val o = JSONObject(text)
            data = Settings(
                opdsBaseURL = o.optString("opdsBaseURL", DEFAULT_OPDS_BASE_URL)
                    .ifBlank { DEFAULT_OPDS_BASE_URL },
                opdsUsername = o.optString("opdsUsername"),
                opdsPassword = o.optString("opdsPassword")
            )
        }
    }

    fun get(): Settings = lock.withLock { data.copy() }

    fun update(next: Settings) {
        lock.withLock {
            data = next.copy(opdsBaseURL = next.opdsBaseURL.trim().trimEnd('/'))
            saveLocked()
        }
    }

    private fun saveLocked() {
        path.parentFile?.mkdirs()
        val o = JSONObject()
            .put("opdsBaseURL", data.opdsBaseURL)
            .put("opdsUsername", data.opdsUsername)
            .put("opdsPassword", data.opdsPassword)
        val tmp = File(path.absolutePath + ".tmp")
        tmp.writeText(o.toString(2))
        if (path.exists()) path.delete()
        tmp.renameTo(path)
    }

    private fun Settings.copy(
        opdsBaseURL: String = this.opdsBaseURL,
        opdsUsername: String = this.opdsUsername,
        opdsPassword: String = this.opdsPassword
    ) = Settings(opdsBaseURL, opdsUsername, opdsPassword)
}

data class OpdsRecord(
    val opdsId: String,
    val title: String = "",
    val localPath: String = "",
    val localBookId: String = "",
    val fingerprint: String = "",
    val contentHash: String = "",
    val size: Long = 0,
    val format: String = ""
)

class OpdsIndex(private val path: File) {
    private val lock = ReentrantLock()
    private var entries: MutableMap<String, OpdsRecord> = mutableMapOf()

    init {
        load()
    }

    private fun load() {
        lock.withLock {
            if (!path.exists()) {
                saveLocked()
                return
            }
            val text = path.readText()
            if (text.isBlank()) return
            val root = JSONObject(text)
            val obj = root.optJSONObject("entries") ?: JSONObject()
            val map = mutableMapOf<String, OpdsRecord>()
            val keys = obj.keys()
            while (keys.hasNext()) {
                val k = keys.next()
                val e = obj.getJSONObject(k)
                map[k] = OpdsRecord(
                    opdsId = e.optString("opdsId", k),
                    title = e.optString("title"),
                    localPath = e.optString("localPath"),
                    localBookId = e.optString("localBookId"),
                    fingerprint = e.optString("fingerprint"),
                    contentHash = e.optString("contentHash"),
                    size = e.optLong("size"),
                    format = e.optString("format")
                )
            }
            entries = map
        }
    }

    fun get(opdsId: String): OpdsRecord? = lock.withLock { entries[opdsId] }

    fun put(rec: OpdsRecord) {
        lock.withLock {
            entries[rec.opdsId] = rec
            saveLocked()
        }
    }

    private fun saveLocked() {
        path.parentFile?.mkdirs()
        val obj = JSONObject()
        entries.forEach { (k, v) ->
            obj.put(
                k,
                JSONObject()
                    .put("opdsId", v.opdsId)
                    .put("title", v.title)
                    .put("localPath", v.localPath)
                    .put("localBookId", v.localBookId)
                    .put("fingerprint", v.fingerprint)
                    .put("contentHash", v.contentHash)
                    .put("size", v.size)
                    .put("format", v.format)
            )
        }
        val root = JSONObject().put("version", 1).put("entries", obj)
        val tmp = File(path.absolutePath + ".tmp")
        tmp.writeText(root.toString(2))
        if (path.exists()) path.delete()
        tmp.renameTo(path)
    }
}
