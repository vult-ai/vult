// vult-ocr reads the text of a scanned statement or receipt on this Mac with
// Apple's Vision text recognition. Nothing leaves the Mac.
//
//   vult-ocr read MEDIATYPE    the file on standard input; the text of each
//                              page as a JSON array of strings on standard
//                              output, lines top to bottom
//
// MEDIATYPE is application/pdf or an image type. A PDF's pages are drawn
// and read one by one.
import CoreGraphics
import Foundation
import ImageIO
import PDFKit
import Vision

enum Exit: Int32 { case usage = 2, unreadable = 3, failed = 5 }

func stop(_ code: Exit, _ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(code.rawValue)
}

let args = CommandLine.arguments
guard args.count == 3, args[1] == "read" else { stop(.usage, "usage: vult-ocr read MEDIATYPE") }
let input = FileHandle.standardInput.readDataToEndOfFile()
guard input.count > 0, input.count <= 40 << 20 else { stop(.usage, "input empty or too large") }

var images: [CGImage] = []
if args[2] == "application/pdf" {
    guard let document = PDFDocument(data: input) else { stop(.unreadable, "not a PDF") }
    for index in 0..<min(document.pageCount, 50) {
        guard let page = document.page(at: index) else { continue }
        let bounds = page.bounds(for: .mediaBox)
        let scale: CGFloat = 2.5
        let width = Int(bounds.width * scale), height = Int(bounds.height * scale)
        guard let context = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                                      space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue) else { continue }
        context.setFillColor(CGColor(red: 1, green: 1, blue: 1, alpha: 1))
        context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        context.scaleBy(x: scale, y: scale)
        page.draw(with: .mediaBox, to: context)
        if let image = context.makeImage() { images.append(image) }
    }
} else {
    guard let source = CGImageSourceCreateWithData(input as CFData, nil) else { stop(.unreadable, "not an image") }
    for index in 0..<min(CGImageSourceGetCount(source), 50) {
        if let image = CGImageSourceCreateImageAtIndex(source, index, nil) { images.append(image) }
    }
}
guard !images.isEmpty else { stop(.unreadable, "no pages") }

// Lines read on the same row are joined left to right, then rows top to
// bottom, so a statement's columns stay on one line.
func text(of image: CGImage) -> String {
    let request = VNRecognizeTextRequest()
    request.recognitionLevel = .accurate
    request.usesLanguageCorrection = true
    request.recognitionLanguages = ["en-US", "pl-PL", "de-DE", "fr-FR", "es-ES"]
    do {
        try VNImageRequestHandler(cgImage: image, options: [:]).perform([request])
    } catch {
        stop(.failed, "recognition failed")
    }
    let found = (request.results ?? []).compactMap { o -> (CGRect, String)? in
        guard let best = o.topCandidates(1).first else { return nil }
        return (o.boundingBox, best.string)
    }
    var rows: [(y: CGFloat, height: CGFloat, items: [(CGFloat, String)])] = []
    for (box, string) in found.sorted(by: { $0.0.midY > $1.0.midY }) {
        if let i = rows.firstIndex(where: { abs($0.y - box.midY) < max($0.height, box.height) * 0.5 }) {
            rows[i].items.append((box.minX, string))
        } else {
            rows.append((box.midY, box.height, [(box.minX, string)]))
        }
    }
    return rows.map { $0.items.sorted(by: { $0.0 < $1.0 }).map { $0.1 }.joined(separator: " ") }.joined(separator: "\n")
}

let pages = images.map(text(of:))
guard let out = try? JSONSerialization.data(withJSONObject: pages) else { stop(.failed, "can't write the result") }
FileHandle.standardOutput.write(out)
