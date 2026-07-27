package com.folio.reader.opds

import android.util.Xml
import okhttp3.Credentials
import okhttp3.OkHttpClient
import okhttp3.Request
import org.xmlpull.v1.XmlPullParser
import java.io.ByteArrayInputStream
import java.net.URL
import java.net.URLEncoder
import java.util.concurrent.TimeUnit

const val REL_ACQUISITION = "http://opds-spec.org/acquisition"
const val REL_IMAGE = "http://opds-spec.org/image"
const val REL_THUMBNAIL = "http://opds-spec.org/image/thumbnail"
const val REL_SUBSECTION = "subsection"
const val REL_NEXT = "next"

data class Acquisition(
    val href: String,
    val type: String = "",
    val length: Long = 0,
    val title: String = ""
) {
    fun formatLabel(): String {
        val t = type.lowercase()
        return when {
            t.contains("epub") -> "epub"
            t.contains("pdf") -> "pdf"
            else -> "bin"
        }
    }
}

data class OpdsEntry(
    val id: String = "",
    val title: String = "",
    val authors: List<String> = emptyList(),
    val summary: String = "",
    val coverURL: String = "",
    val thumbnailURL: String = "",
    val acquisitions: List<Acquisition> = emptyList(),
    val isNavigation: Boolean = false,
    val navURL: String = ""
) {
    fun preferredAcquisition(): Acquisition? {
        if (acquisitions.isEmpty()) return null
        acquisitions.firstOrNull { it.formatLabel() == "epub" }?.let { return it }
        acquisitions.firstOrNull { it.formatLabel() == "pdf" }?.let { return it }
        return acquisitions.first()
    }
}

data class OpdsFeed(
    val title: String = "",
    var selfURL: String = "",
    var nextURL: String = "",
    val entries: List<OpdsEntry> = emptyList()
) {
    fun bookCount(): Int = entries.count { it.acquisitions.isNotEmpty() }

    fun findBooksNavHref(): String {
        for (e in entries) {
            if (!e.isNavigation || e.navURL.isBlank()) continue
            val low = (e.title + " " + e.navURL).lowercase()
            if (low.contains("book") || low.contains("/books")) return e.navURL
        }
        return entries.firstOrNull { it.isNavigation && it.navURL.isNotBlank() }?.navURL.orEmpty()
    }
}

class OpdsClient(
    baseURL: String,
    private val username: String = "",
    private val password: String = ""
) {
    val baseURL: String = baseURL.trim().trimEnd('/')

    private val http = OkHttpClient.Builder()
        .connectTimeout(30, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .followRedirects(true)
        .build()

    fun resolve(href: String): String {
        val h = href.trim()
        if (h.isEmpty()) throw IllegalArgumentException("empty href")
        if (baseURL.isEmpty()) throw IllegalArgumentException("OPDS base URL is not configured")
        if (h.startsWith("http://") || h.startsWith("https://")) return h
        return URL(URL("$baseURL/"), h).toString()
    }

    fun fetch(pathOrURL: String): OpdsFeed {
        if (baseURL.isEmpty()) throw IllegalArgumentException("OPDS base URL is not configured")
        val abs = when {
            pathOrURL.isBlank() -> "$baseURL/opds"
            pathOrURL.startsWith("http://") || pathOrURL.startsWith("https://") -> pathOrURL
            pathOrURL.startsWith("/") -> baseURL + pathOrURL
            else -> resolve(pathOrURL)
        }
        val reqBuilder = Request.Builder()
            .url(abs)
            .header("Accept", "application/atom+xml, application/xml, text/xml, */*")
            .header("User-Agent", "Folio-OPDS/1.0")
            .get()
        if (username.isNotBlank()) {
            reqBuilder.header("Authorization", Credentials.basic(username, password))
        }
        http.newCall(reqBuilder.build()).execute().use { res ->
            if (!res.isSuccessful) {
                val body = res.body?.string()?.take(512).orEmpty()
                throw IllegalStateException("OPDS HTTP ${res.code}: $body")
            }
            val data = res.body?.bytes() ?: ByteArray(0)
            val feed = parseFeed(data)
            feed.selfURL = abs
            val feedBase = URL(abs)
            val resolved = feed.entries.map { e ->
                e.copy(
                    coverURL = resolveAgainst(feedBase, e.coverURL),
                    thumbnailURL = resolveAgainst(feedBase, e.thumbnailURL),
                    acquisitions = e.acquisitions.map { a ->
                        a.copy(href = resolveAgainst(feedBase, a.href))
                    },
                    navURL = resolveAgainst(feedBase, e.navURL)
                )
            }
            if (feed.nextURL.isNotBlank()) {
                feed.nextURL = resolveAgainst(feedBase, feed.nextURL)
            }
            return feed.copy(entries = resolved)
        }
    }

    fun fetchBooksRoot(): OpdsFeed {
        val candidates = listOf(
            "/opds/new",
            "/opds/newest",
            "/opds/books/letter/00",
            "/opds/books",
            "/opds"
        )
        var lastErr: Exception? = null
        for (p in candidates) {
            try {
                val feed = fetch(p)
                if (feed.bookCount() > 0) return feed
                val next = feed.findBooksNavHref()
                if (next.isNotBlank()) {
                    val feed2 = fetch(next)
                    if (feed2.bookCount() > 0) return feed2
                    for (e in feed2.entries) {
                        if (e.isNavigation && e.navURL.isNotBlank()) {
                            val feed3 = fetch(e.navURL)
                            if (feed3.bookCount() > 0) return feed3
                        }
                    }
                }
            } catch (e: Exception) {
                lastErr = e
            }
        }
        throw lastErr ?: IllegalStateException("could not find OPDS book list")
    }

    fun search(query: String): OpdsFeed {
        val q = query.trim()
        if (q.isEmpty()) return fetchBooksRoot()
        val enc = URLEncoder.encode(q, "UTF-8").replace("+", "%20")
        val candidates = listOf(
            "/opds/search/$enc",
            "/opds/search?query=" + URLEncoder.encode(q, "UTF-8"),
            "/opds/search?q=" + URLEncoder.encode(q, "UTF-8")
        )
        var lastErr: Exception? = null
        for (p in candidates) {
            try {
                return fetch(p)
            } catch (e: Exception) {
                lastErr = e
            }
        }
        throw lastErr ?: IllegalStateException("search failed")
    }

    fun download(href: String): okhttp3.Response {
        val abs = if (href.startsWith("http://") || href.startsWith("https://")) href else resolve(href)
        val reqBuilder = Request.Builder()
            .url(abs)
            .header("User-Agent", "Folio-OPDS/1.0")
            .get()
        if (username.isNotBlank()) {
            reqBuilder.header("Authorization", Credentials.basic(username, password))
        }
        val res = http.newCall(reqBuilder.build()).execute()
        if (!res.isSuccessful) {
            res.close()
            throw IllegalStateException("download HTTP ${res.code}")
        }
        return res
    }

    companion object {
        private fun resolveAgainst(base: URL, href: String): String {
            val h = href.trim()
            if (h.isEmpty()) return h
            if (h.startsWith("http://") || h.startsWith("https://")) return h
            return try {
                URL(base, h).toString()
            } catch (_: Exception) {
                h
            }
        }

        fun parseFeed(data: ByteArray): OpdsFeed {
            val parser = Xml.newPullParser()
            parser.setFeature(XmlPullParser.FEATURE_PROCESS_NAMESPACES, true)
            parser.setInput(ByteArrayInputStream(data), "UTF-8")

            var title = ""
            var nextURL = ""
            val entries = mutableListOf<OpdsEntry>()

            var inEntry = false
            var eId = ""
            var eTitle = ""
            var eSummary = ""
            val eAuthors = mutableListOf<String>()
            var eCover = ""
            var eThumb = ""
            val eAcq = mutableListOf<Acquisition>()
            var eNav = ""
            var eIsNav = false

            var event = parser.eventType
            while (event != XmlPullParser.END_DOCUMENT) {
                when (event) {
                    XmlPullParser.START_TAG -> {
                        val local = parser.name?.lowercase() ?: ""
                        when {
                            local == "feed" -> {}
                            local == "entry" -> {
                                inEntry = true
                                eId = ""; eTitle = ""; eSummary = ""
                                eAuthors.clear(); eCover = ""; eThumb = ""
                                eAcq.clear(); eNav = ""; eIsNav = false
                            }
                            !inEntry && local == "title" -> title = parser.nextText().trim()
                            !inEntry && local == "link" -> {
                                val rel = attr(parser, "rel")
                                val href = attr(parser, "href")
                                if (rel.contains(REL_NEXT) || rel == "next") nextURL = href
                            }
                            inEntry && local == "id" -> eId = parser.nextText().trim()
                            inEntry && local == "title" -> eTitle = parser.nextText().trim()
                            inEntry && local == "summary" -> eSummary = parser.nextText().trim()
                            inEntry && local == "content" -> {
                                val t = parser.nextText().trim()
                                if (eSummary.isBlank()) eSummary = t
                            }
                            inEntry && local == "name" -> eAuthors.add(parser.nextText().trim())
                            inEntry && local == "link" -> {
                                val rel = attr(parser, "rel")
                                val href = attr(parser, "href")
                                val type = attr(parser, "type")
                                val length = attr(parser, "length").toLongOrNull() ?: 0L
                                val linkTitle = attr(parser, "title")
                                when {
                                    rel.contains("acquisition") || rel == REL_ACQUISITION ||
                                        type.contains("epub") || type.contains("pdf") -> {
                                        eAcq.add(Acquisition(href, type, length, linkTitle))
                                    }
                                    rel.contains("thumbnail") || rel == REL_THUMBNAIL -> eThumb = href
                                    rel.contains("image") || rel == REL_IMAGE -> eCover = href
                                    rel == REL_SUBSECTION || rel == "subsection" ||
                                        type.contains("atom") && href.isNotBlank() -> {
                                        eIsNav = true
                                        eNav = href
                                    }
                                    rel == "alternate" && type.contains("atom") -> {
                                        eIsNav = true
                                        eNav = href
                                    }
                                }
                            }
                        }
                    }
                    XmlPullParser.END_TAG -> {
                        if (parser.name.equals("entry", true) && inEntry) {
                            if (eAcq.isEmpty() && eNav.isNotBlank()) eIsNav = true
                            entries.add(
                                OpdsEntry(
                                    id = eId,
                                    title = eTitle,
                                    authors = eAuthors.toList(),
                                    summary = eSummary,
                                    coverURL = eCover,
                                    thumbnailURL = eThumb,
                                    acquisitions = eAcq.toList(),
                                    isNavigation = eIsNav && eAcq.isEmpty(),
                                    navURL = eNav
                                )
                            )
                            inEntry = false
                        }
                    }
                }
                event = parser.next()
            }
            return OpdsFeed(title = title, nextURL = nextURL, entries = entries)
        }

        private fun attr(parser: XmlPullParser, name: String): String {
            for (i in 0 until parser.attributeCount) {
                if (parser.getAttributeName(i).equals(name, true)) {
                    return parser.getAttributeValue(i) ?: ""
                }
            }
            // also try namespaced
            return parser.getAttributeValue(null, name) ?: ""
        }
    }
}
