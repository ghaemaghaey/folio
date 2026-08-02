package com.folio.reader.epub

import android.util.Base64
import android.util.Xml
import org.xmlpull.v1.XmlPullParser
import java.io.ByteArrayInputStream
import java.io.File
import java.util.zip.ZipFile

data class SpineItem(
    val id: String,
    val href: String,
    val mediaType: String,
    var label: String = ""
)

data class Chapter(
    val index: Int,
    val label: String,
    val html: String
)

data class TocItem(
    val index: Int,
    val label: String,
    val href: String
)

class EpubBook private constructor(
    val title: String,
    val language: String,
    val spine: List<SpineItem>,
    private val files: Map<String, ByteArray>,
    val coverImagePath: String = ""
) {
    fun chapterCount(): Int = spine.size

    /** Extract cover image bytes from the epub zip, or null if no cover. */
    fun extractCover(): ByteArray? {
        if (coverImagePath.isEmpty()) return null
        val key = coverImagePath.lowercase()
        return files[key] ?: files["./$key"] ?: run {
            // Try without leading ./
            val clean = key.removePrefix("./")
            files[clean]
        }
    }

    /** Return a base64 data URL for the cover image, or empty string. */
    fun coverDataURL(): String {
        val data = extractCover() ?: return ""
        val mediaType = when {
            data.size > 8 && data[0] == 0x89.toByte() && data[1] == 0x50.toByte() -> "image/png"
            data.size > 3 && data[0] == 0x47.toByte() && data[1] == 0x49.toByte() -> "image/gif"
            data.size > 3 && data[0] == 0x52.toByte() && data[1] == 0x49.toByte() -> "image/webp"
            else -> "image/jpeg"
        }
        return "data:$mediaType;base64,${Base64.encodeToString(data, Base64.NO_WRAP)}"
    }

    fun toc(): List<TocItem> = spine.mapIndexed { i, s ->
        val label = s.label.trim().ifEmpty { "Chapter ${i + 1}" }
        TocItem(i, label, s.href)
    }

    fun resolveHref(fromChapter: Int, href: String): Triple<Int, String, Boolean> {
        var h = href.trim()
        if (h.isEmpty()) return Triple(0, "", false)
        if (h.startsWith("#")) return Triple(fromChapter, h.removePrefix("#"), true)
        var frag = ""
        val hash = h.indexOf('#')
        if (hash >= 0) {
            frag = h.substring(hash + 1)
            h = h.substring(0, hash)
        }
        val base = if (fromChapter in spine.indices) dirname(spine[fromChapter].href) else ""
        val target = cleanPath(joinPath(base, h)).trimStart('/')
        spine.forEachIndexed { i, s ->
            if (s.href.equals(target, true) || basename(s.href) == basename(target)) {
                return Triple(i, frag, true)
            }
        }
        return Triple(0, "", false)
    }

    fun getChapter(index: Int): Chapter {
        if (index !in spine.indices) throw IllegalArgumentException("chapter out of range")
        val item = spine[index]
        val raw = readFile(item.href) ?: throw IllegalArgumentException("missing chapter: ${item.href}")
        val xhtml = String(raw, Charsets.UTF_8)
        var html = extractBody(xhtml)
        html = rewriteImageSources(html, dirname(item.href))
        var label = item.label
        if (label.isBlank() || sameTitle(label, title)) {
            val h = firstHeading(xhtml)
            label = if (h.isNotBlank() && !sameTitle(h, title)) h else "Chapter ${index + 1}"
        }
        return Chapter(index, label, html)
    }

    private fun readFile(p: String): ByteArray? {
        val key = cleanPath(p).trimStart('/')
        return files[key] ?: files[key.lowercase()]
    }

    private fun rewriteImageSources(html: String, base: String): String {
        val lower = html.lowercase()
        val out = StringBuilder()
        var rest = html
        var restLower = lower
        while (true) {
            val i = restLower.indexOf("src=")
            if (i < 0) {
                out.append(rest)
                break
            }
            out.append(rest, 0, i + 4)
            rest = rest.substring(i + 4)
            restLower = rest.lowercase()
            if (rest.isEmpty()) break
            val quote = rest[0]
            if (quote != '"' && quote != '\'') continue
            rest = rest.substring(1)
            restLower = rest.lowercase()
            val j = rest.indexOf(quote)
            if (j < 0) {
                out.append(quote).append(rest)
                break
            }
            val src = rest.substring(0, j)
            rest = rest.substring(j) // includes quote
            restLower = rest.lowercase()
            if (src.startsWith("data:") || src.startsWith("http://") || src.startsWith("https://")) {
                out.append(quote).append(src)
                continue
            }
            val full = cleanPath(joinPath(base, src))
            val data = readFile(full)
            if (data != null) {
                out.append(quote)
                    .append("data:")
                    .append(mimeFromPath(full))
                    .append(";base64,")
                    .append(Base64.encodeToString(data, Base64.NO_WRAP))
            } else {
                out.append(quote).append(src)
            }
        }
        return out.toString()
    }

    companion object {
        fun open(filePath: String): EpubBook {
            val zip = ZipFile(File(filePath))
            val files = mutableMapOf<String, ByteArray>()
            zip.use { z ->
                val entries = z.entries()
                while (entries.hasMoreElements()) {
                    val e = entries.nextElement()
                    if (e.isDirectory) continue
                    val name = cleanPath(e.name.replace('\\', '/'))
                    val data = z.getInputStream(e).readBytes()
                    files[name] = data
                    files[name.lowercase()] = data
                }
            }

            val container = files["META-INF/container.xml"]
                ?: files["meta-inf/container.xml"]
                ?: throw IllegalArgumentException("missing META-INF/container.xml")
            val opfPath = parseRootfile(container)
            val opfDir = dirname(opfPath)
            val opfData = files[opfPath] ?: files[opfPath.lowercase()]
                ?: throw IllegalArgumentException("missing OPF: $opfPath")

            val (metaTitle, metaLang, manifest, spineIds, coverPath) = parseOpf(opfData)
            val spine = mutableListOf<SpineItem>()
            for (id in spineIds) {
                val item = manifest[id] ?: continue
                var href = item.href
                if (opfDir.isNotEmpty() && !href.startsWith("/")) {
                    href = joinPath(opfDir, href)
                }
                href = cleanPath(href)
                spine.add(SpineItem(id, href, item.mediaType))
            }
            if (spine.isEmpty()) throw IllegalArgumentException("EPUB has no chapters")

            var title = metaTitle.ifBlank { "Untitled" }
            val labels = collectTocLabels(files, opfDir, manifest)
            spine.forEachIndexed { i, s ->
                val lab = lookupLabel(labels, s.href)
                when {
                    lab.isNotBlank() && !sameTitle(lab, title) -> s.label = lab
                    else -> {
                        val raw = files[s.href] ?: files[s.href.lowercase()]
                        val h = if (raw != null) firstHeading(String(raw, Charsets.UTF_8)) else ""
                        s.label = when {
                            h.isNotBlank() && !sameTitle(h, title) -> h
                            else -> {
                                val base = basename(s.href).substringBeforeLast('.', basename(s.href))
                                    .replace('_', ' ').replace('-', ' ').trim()
                                if (base.isNotBlank() && !sameTitle(base, title)) base else "Chapter ${i + 1}"
                            }
                        }
                    }
                }
            }
            // Resolve cover path relative to OPF directory
            val resolvedCoverPath = if (coverPath.isNotEmpty()) {
                val p = if (opfDir.isNotEmpty() && !coverPath.startsWith("/")) {
                    cleanPath(joinPath(opfDir, coverPath))
                } else {
                    cleanPath(coverPath)
                }
                p
            } else ""

            return EpubBook(title, metaLang, spine, files, resolvedCoverPath)
        }

        private data class ManifestItem(
            val id: String,
            val href: String,
            val mediaType: String,
            val properties: String
        )

        private data class OpfResult(
            val title: String,
            val language: String,
            val manifest: Map<String, ManifestItem>,
            val spineIds: List<String>,
            val coverPath: String = ""
        )

        private fun parseRootfile(xml: ByteArray): String {
            val parser = Xml.newPullParser()
            parser.setFeature(XmlPullParser.FEATURE_PROCESS_NAMESPACES, true)
            parser.setInput(ByteArrayInputStream(xml), "UTF-8")
            var event = parser.eventType
            while (event != XmlPullParser.END_DOCUMENT) {
                if (event == XmlPullParser.START_TAG &&
                    parser.name.equals("rootfile", true)
                ) {
                    val fp = parser.getAttributeValue(null, "full-path")
                        ?: parser.getAttributeValue(null, "fullpath")
                    if (!fp.isNullOrBlank()) return cleanPath(fp)
                }
                event = parser.next()
            }
            throw IllegalArgumentException("no rootfile in container")
        }

        private fun parseOpf(data: ByteArray): OpfResult {
            // Normalize common prefixes for simpler matching
            var text = String(data, Charsets.UTF_8)
            text = text.replace("dc:title", "title").replace("dc:language", "language")
                .replace("opf:", "")
            val parser = Xml.newPullParser()
            parser.setFeature(XmlPullParser.FEATURE_PROCESS_NAMESPACES, false)
            parser.setInput(ByteArrayInputStream(text.toByteArray(Charsets.UTF_8)), "UTF-8")

            var title = ""
            var language = ""
            val manifest = mutableMapOf<String, ManifestItem>()
            val spineIds = mutableListOf<String>()
            var inManifest = false
            var inSpine = false
            var inMetadata = false
            var coverContentId = ""

            var event = parser.eventType
            while (event != XmlPullParser.END_DOCUMENT) {
                when (event) {
                    XmlPullParser.START_TAG -> {
                        val name = parser.name?.lowercase() ?: ""
                        when (name) {
                            "metadata" -> inMetadata = true
                            "manifest" -> inManifest = true
                            "spine" -> inSpine = true
                            "title" -> if (inMetadata && title.isEmpty()) {
                                title = parser.nextText().trim()
                            }
                            "language" -> if (inMetadata && language.isEmpty()) {
                                language = parser.nextText().trim()
                            }
                            "meta" -> if (inMetadata) {
                                val name = parser.getAttributeValue(null, "name") ?: ""
                                val content = parser.getAttributeValue(null, "content") ?: ""
                                if (name.equals("cover", ignoreCase = true) && content.isNotEmpty()) {
                                    coverContentId = content
                                }
                            }
                            "item" -> if (inManifest) {
                                val id = parser.getAttributeValue(null, "id") ?: ""
                                val href = parser.getAttributeValue(null, "href") ?: ""
                                val mt = parser.getAttributeValue(null, "media-type")
                                    ?: parser.getAttributeValue(null, "mediaType") ?: ""
                                val props = parser.getAttributeValue(null, "properties") ?: ""
                                if (id.isNotEmpty()) {
                                    manifest[id] = ManifestItem(id, href, mt, props)
                                }
                            }
                            "itemref" -> if (inSpine) {
                                val idref = parser.getAttributeValue(null, "idref")
                                if (!idref.isNullOrBlank()) spineIds.add(idref)
                            }
                        }
                    }
                    XmlPullParser.END_TAG -> {
                        when (parser.name?.lowercase()) {
                            "metadata" -> inMetadata = false
                            "manifest" -> inManifest = false
                            "spine" -> inSpine = false
                        }
                    }
                }
                event = parser.next()
            }
            // Detect cover image
            var coverPath = ""
            // EPUB2: <meta name="cover" content="cover-image-id"/>
            if (coverContentId.isNotEmpty()) {
                manifest[coverContentId]?.let { coverPath = it.href }
            }
            // EPUB3: manifest item with properties="cover-image"
            if (coverPath.isEmpty()) {
                for (it in manifest.values) {
                    if (it.properties.contains("cover-image")) {
                        coverPath = it.href
                        break
                    }
                }
            }

            return OpfResult(title, language, manifest, spineIds, coverPath)
        }

        private fun collectTocLabels(
            files: Map<String, ByteArray>,
            opfDir: String,
            manifest: Map<String, ManifestItem>
        ): Map<String, String> {
            val out = mutableMapOf<String, String>()
            for (it in manifest.values) {
                val mt = it.mediaType.lowercase()
                val props = it.properties.lowercase()
                val href = if (opfDir.isNotEmpty()) cleanPath(joinPath(opfDir, it.href)) else cleanPath(it.href)
                if (props.contains("nav") || (mt.contains("xhtml") && it.href.lowercase().contains("nav"))) {
                    files[href]?.let { data ->
                        out.putAll(parseNavHtml(String(data, Charsets.UTF_8), dirname(href)))
                    }
                }
                if (mt.contains("ncx") || it.href.lowercase().endsWith(".ncx")) {
                    files[href]?.let { data ->
                        out.putAll(parseNcx(data, dirname(href)))
                    }
                }
            }
            val candidates = listOf(
                joinPath(opfDir, "toc.ncx"),
                joinPath(opfDir, "nav.xhtml"),
                "toc.ncx",
                "OEBPS/toc.ncx",
                "OEBPS/nav.xhtml"
            )
            for (cand in candidates) {
                val c = cleanPath(cand)
                val data = files[c] ?: files[c.lowercase()] ?: continue
                if (c.lowercase().endsWith(".ncx")) {
                    out.putAll(parseNcx(data, dirname(c)))
                } else {
                    out.putAll(parseNavHtml(String(data, Charsets.UTF_8), dirname(c)))
                }
            }
            return out
        }

        private fun parseNcx(data: ByteArray, baseDir: String): Map<String, String> {
            val out = mutableMapOf<String, String>()
            val parser = Xml.newPullParser()
            parser.setFeature(XmlPullParser.FEATURE_PROCESS_NAMESPACES, false)
            parser.setInput(ByteArrayInputStream(data), "UTF-8")
            var inLabel = false
            var inText = false
            var label = ""
            var event = parser.eventType
            while (event != XmlPullParser.END_DOCUMENT) {
                when (event) {
                    XmlPullParser.START_TAG -> {
                        when (parser.name?.lowercase()) {
                            "navlabel" -> {
                                inLabel = true
                                label = ""
                            }
                            "text" -> if (inLabel) inText = true
                            "content" -> {
                                val src = parser.getAttributeValue(null, "src")?.substringBefore('#') ?: ""
                                if (src.isNotBlank() && label.isNotBlank()) {
                                    val full = cleanPath(joinPath(baseDir, src))
                                    out[full] = label.trim()
                                    out[basename(full)] = label.trim()
                                }
                            }
                        }
                    }
                    XmlPullParser.TEXT -> if (inText) label += parser.text
                    XmlPullParser.END_TAG -> {
                        when (parser.name?.lowercase()) {
                            "text" -> inText = false
                            "navlabel" -> inLabel = false
                        }
                    }
                }
                event = parser.next()
            }
            return out
        }

        private fun parseNavHtml(html: String, baseDir: String): Map<String, String> {
            val out = mutableMapOf<String, String>()
            val lower = html.lowercase()
            var segment = html
            val tocIdx = lower.indexOf("epub:type=\"toc\"").takeIf { it >= 0 }
                ?: lower.indexOf("epub:type='toc'")
            if (tocIdx >= 0) {
                val start = lower.lastIndexOf("<nav", tocIdx)
                val end = lower.indexOf("</nav>", tocIdx)
                if (start >= 0 && end > start) segment = html.substring(start, end + 6)
            }
            var rest = segment
            while (true) {
                val low = rest.lowercase()
                val a = low.indexOf("<a ")
                if (a < 0) break
                val gt = rest.indexOf('>', a)
                if (gt < 0) break
                val tag = rest.substring(a, gt + 1)
                val href = extractAttr(tag, "href")
                val close = low.indexOf("</a>", gt)
                if (close < 0 || href.isNullOrBlank()) {
                    rest = rest.substring(gt + 1)
                    continue
                }
                val label = stripTags(rest.substring(gt + 1, close)).trim()
                val src = href.substringBefore('#')
                if (src.isNotBlank() && label.isNotBlank()) {
                    val full = cleanPath(joinPath(baseDir, src))
                    out[full] = label
                    out[basename(full)] = label
                }
                rest = rest.substring(close + 4)
            }
            return out
        }

        private fun lookupLabel(labels: Map<String, String>, href: String): String {
            val h = cleanPath(href)
            labels[h]?.let { return it }
            labels[basename(h)]?.let { return it }
            for ((k, v) in labels) {
                if (basename(k) == basename(h)) return v
            }
            return ""
        }

        private fun extractAttr(tag: String, name: String): String? {
            val re = Regex("""$name\s*=\s*(['"])(.*?)\1""", RegexOption.IGNORE_CASE)
            return re.find(tag)?.groupValues?.getOrNull(2)
        }

        fun extractBody(xhtml: String): String {
            val lower = xhtml.lowercase()
            val startBody = lower.indexOf("<body")
            if (startBody < 0) return sanitizeFragment(xhtml)
            val gt = xhtml.indexOf('>', startBody)
            if (gt < 0) return sanitizeFragment(xhtml)
            val start = gt + 1
            val end = lower.lastIndexOf("</body>")
            return if (end > start) sanitizeFragment(xhtml.substring(start, end))
            else sanitizeFragment(xhtml.substring(start))
        }

        fun sanitizeFragment(s: String): String {
            var out = s
            while (true) {
                val l = out.lowercase()
                val a = l.indexOf("<script")
                if (a < 0) break
                val b = l.indexOf("</script>", a)
                out = if (b < 0) out.substring(0, a)
                else out.substring(0, a) + out.substring(b + 9)
            }
            return out.trim()
        }

        fun firstHeading(xhtml: String): String {
            val lower = xhtml.lowercase()
            for (tag in listOf("h1", "h2", "h3")) {
                val open = "<$tag"
                val i = lower.indexOf(open)
                if (i < 0) continue
                val gt = xhtml.indexOf('>', i)
                if (gt < 0) continue
                val close = lower.indexOf("</$tag", gt)
                if (close < 0) continue
                var text = stripTags(xhtml.substring(gt + 1, close)).replace(Regex("\\s+"), " ").trim()
                if (text.length > 80) text = text.take(80) + "…"
                if (text.isNotBlank()) return text
            }
            return ""
        }

        fun stripTags(s: String): String {
            val b = StringBuilder()
            var inTag = false
            for (r in s) {
                when {
                    r == '<' -> inTag = true
                    r == '>' -> inTag = false
                    !inTag -> b.append(r)
                }
            }
            return b.toString()
        }

        fun sameTitle(a: String, b: String): Boolean {
            val na = a.trim().lowercase().split(Regex("\\s+")).joinToString(" ")
            val nb = b.trim().lowercase().split(Regex("\\s+")).joinToString(" ")
            return na.isNotEmpty() && na == nb
        }

        fun cleanPath(p: String): String {
            val parts = p.replace('\\', '/').split('/')
            val stack = ArrayList<String>()
            for (part in parts) {
                when {
                    part.isEmpty() || part == "." -> {}
                    part == ".." -> if (stack.isNotEmpty()) stack.removeAt(stack.lastIndex)
                    else -> stack.add(part)
                }
            }
            return stack.joinToString("/")
        }

        fun dirname(p: String): String {
            val idx = p.replace('\\', '/').lastIndexOf('/')
            return if (idx <= 0) "" else p.substring(0, idx)
        }

        fun basename(p: String): String = p.replace('\\', '/').substringAfterLast('/')

        fun joinPath(base: String, rel: String): String {
            if (rel.startsWith("/")) return rel.trimStart('/')
            if (base.isEmpty()) return rel
            return "$base/$rel"
        }

        fun mimeFromPath(p: String): String {
            return when (p.substringAfterLast('.', "").lowercase()) {
                "png" -> "image/png"
                "jpg", "jpeg" -> "image/jpeg"
                "gif" -> "image/gif"
                "svg" -> "image/svg+xml"
                "webp" -> "image/webp"
                "css" -> "text/css"
                else -> "application/octet-stream"
            }
        }
    }
}
