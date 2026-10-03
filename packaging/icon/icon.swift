// Draws the app icon, a 1024 px PNG, in the style of loop's: a dark glyph
// on a white tile, over the name. The glyph is a pointer, with a sparkle
// for the agent driving it. Run by "make icon".
//
//	swift packaging/icon/icon.swift out.png
import AppKit

let size = 1024.0
let out = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "icon.png"
let srgb = CGColorSpace(name: CGColorSpace.sRGB)!

let ctx = CGContext(data: nil, width: Int(size), height: Int(size), bitsPerComponent: 8, bytesPerRow: 0,
                    space: srgb, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
// Top-left origin, as the coordinates below are written.
ctx.translateBy(x: 0, y: size)
ctx.scaleBy(x: 1, y: -1)

func gray(_ v: Double, _ a: Double = 1) -> CGColor { CGColor(srgbRed: v, green: v, blue: v, alpha: a) }
let ink = gray(0x22 / 255.0, 0.9)

func vertical(_ colors: [CGColor], _ top: Double, _ bottom: Double) {
    let g = CGGradient(colorsSpace: srgb, colors: colors as CFArray, locations: nil)!
    ctx.drawLinearGradient(g, start: CGPoint(x: 0, y: top), end: CGPoint(x: 0, y: bottom), options: [])
}

// The tile on Apple's icon grid: 824 pt inside a 1024 pt canvas.
let tile = CGRect(x: 100, y: 100, width: 824, height: 824)
let tilePath = CGPath(roundedRect: tile, cornerWidth: 185, cornerHeight: 185, transform: nil)

ctx.saveGState()
ctx.addPath(tilePath)
ctx.clip()
vertical([gray(1), gray(0xE8 / 255.0)], tile.minY, tile.maxY)
ctx.restoreGState()

// A thin edge, darker toward the bottom.
ctx.saveGState()
ctx.addPath(tilePath)
ctx.setLineWidth(2)
ctx.replacePathWithStrokedPath()
ctx.clip()
vertical([gray(0xCC / 255.0, 0.5), gray(0xBB / 255.0, 0.5), gray(0xAA / 255.0, 0.5)], tile.minY, tile.maxY)
ctx.restoreGState()

// Ink with a soft glow, as loop's glyph has.
func inked(_ draw: () -> Void) {
    ctx.saveGState()
    ctx.setShadow(offset: .zero, blur: 10, color: gray(0x22 / 255.0, 0.6))
    draw()
    ctx.restoreGState()
}

// The pointer, stroked like loop's infinity sign.
let tip = CGPoint(x: 392, y: 214)
let h = 330.0
let arrow: [(Double, Double)] = [
    (0, 0), (0, 0.74), (0.17, 0.585), (0.29, 0.86), (0.405, 0.81), (0.285, 0.54), (0.52, 0.54),
]
let pointer = CGMutablePath()
pointer.addLines(between: arrow.map { CGPoint(x: tip.x + $0.0 * h, y: tip.y + $0.1 * h) })
pointer.closeSubpath()
inked {
    ctx.addPath(pointer)
    ctx.setLineWidth(30)
    ctx.setLineJoin(.round)
    ctx.setLineCap(.round)
    ctx.setStrokeColor(ink)
    ctx.strokePath()
}

// The sparkle: a four-point star.
func star(_ c: CGPoint, _ r: Double) {
    let p = CGMutablePath()
    let k = 0.2
    p.move(to: CGPoint(x: c.x, y: c.y - r))
    p.addQuadCurve(to: CGPoint(x: c.x + r, y: c.y), control: CGPoint(x: c.x + r * k, y: c.y - r * k))
    p.addQuadCurve(to: CGPoint(x: c.x, y: c.y + r), control: CGPoint(x: c.x + r * k, y: c.y + r * k))
    p.addQuadCurve(to: CGPoint(x: c.x - r, y: c.y), control: CGPoint(x: c.x - r * k, y: c.y + r * k))
    p.addQuadCurve(to: CGPoint(x: c.x, y: c.y - r), control: CGPoint(x: c.x - r * k, y: c.y - r * k))
    inked {
        ctx.addPath(p)
        ctx.setFillColor(ink)
        ctx.fillPath()
    }
}
star(CGPoint(x: 618, y: 256), 62)
star(CGPoint(x: 552, y: 188), 26)

// The name, bold and rounded like loop's.
let base = NSFont.systemFont(ofSize: 150, weight: .bold)
let font = base.fontDescriptor.withDesign(.rounded).flatMap { NSFont(descriptor: $0, size: 150) } ?? base
let name = NSAttributedString(string: "MacUse", attributes: [
    .font: font,
    .foregroundColor: NSColor(cgColor: ink)!,
])
let line = CTLineCreateWithAttributedString(name)
let width = CTLineGetTypographicBounds(line, nil, nil, nil)
ctx.saveGState()
// Text draws upward from its baseline, so undo the flip around it.
let baseline = 730.0
ctx.translateBy(x: (size - width) / 2, y: baseline)
ctx.scaleBy(x: 1, y: -1)
ctx.textPosition = .zero
CTLineDraw(line, ctx)
ctx.restoreGState()

let rep = NSBitmapImageRep(cgImage: ctx.makeImage()!)
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: out))
