import Foundation

/// A resolved mascot pack: the JSON shape of ai-mascot's `ResolvedPack` (crates/mascot-pack
/// resolve.rs), which the Go core emits in `dp_pack_json`. A resolved pack always carries
/// every field; the decoder still applies the resolver's defaults (loop/bob true, faceDy all
/// zeros, bob {px 0, frames 1}) so an unresolved manifest decodes to the same value.
public struct Pack: Decodable, Sendable, Equatable {
    public struct Bob: Decodable, Sendable, Equatable {
        public let px: Int
        public let frames: Int

        public init(px: Int, frames: Int) {
            self.px = px
            self.frames = frames
        }
    }

    public struct StateSpec: Decodable, Sendable, Equatable {
        public let row, frames: Int
        public let loop, bob: Bool
        /// Vertical face (mouth) offset per frame, in cell pixels; one entry per frame.
        public let faceDy: [Int]

        public init(row: Int, frames: Int, loop: Bool, bob: Bool, faceDy: [Int]) {
            self.row = row
            self.frames = frames
            self.loop = loop
            self.bob = bob
            self.faceDy = faceDy
        }

        private enum CodingKeys: String, CodingKey { case row, frames, loop, bob, faceDy }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            row = try c.decode(Int.self, forKey: .row)
            frames = try c.decode(Int.self, forKey: .frames)
            loop = try c.decodeIfPresent(Bool.self, forKey: .loop) ?? true
            bob = try c.decodeIfPresent(Bool.self, forKey: .bob) ?? true
            faceDy = try c.decodeIfPresent([Int].self, forKey: .faceDy) ?? Array(repeating: 0, count: max(frames, 0))
        }
    }

    public struct Mouth: Decodable, Sendable, Equatable {
        public let row: Int
        /// [x, y] of the mouth's top-left corner inside the cell, in cell pixels.
        public let anchor, size: [Int]
        public let levels: Int

        public init(row: Int, anchor: [Int], size: [Int], levels: Int) {
            self.row = row
            self.anchor = anchor
            self.size = size
            self.levels = levels
        }
    }

    public let name: String
    /// [width, height] of one cell in sheet pixels.
    public let cell: [Int]
    public let fps: Int
    public let bob: Bob
    public let states: [String: StateSpec]
    public let mouth: Mouth

    public init(name: String, cell: [Int], fps: Int, bob: Bob, states: [String: StateSpec], mouth: Mouth) {
        self.name = name
        self.cell = cell
        self.fps = fps
        self.bob = bob
        self.states = states
        self.mouth = mouth
    }

    private enum CodingKeys: String, CodingKey { case name, cell, fps, bob, states, mouth }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        cell = try c.decode([Int].self, forKey: .cell)
        fps = try c.decode(Int.self, forKey: .fps)
        bob = try c.decodeIfPresent(Bob.self, forKey: .bob) ?? Bob(px: 0, frames: 1)
        states = try c.decode([String: StateSpec].self, forKey: .states)
        mouth = try c.decode(Mouth.self, forKey: .mouth)
        guard cell.count == 2, mouth.anchor.count == 2, mouth.size.count == 2 else {
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath,
                                                    debugDescription: "cell, mouth.anchor and mouth.size need 2 entries"))
        }
    }

    public var cellWidth: Int { cell[0] }
    public var cellHeight: Int { cell[1] }
}

/// What `CoreEngine.packJSON()` returns: `{"pack": ResolvedPack, "sheetPath": "/abs/sheet.png"}`.
public struct PackPayload: Decodable, Sendable, Equatable {
    public let pack: Pack
    public let sheetPath: String

    public init(pack: Pack, sheetPath: String) {
        self.pack = pack
        self.sheetPath = sheetPath
    }

    /// nil for malformed JSON or a missing field.
    public static func decode(_ json: Data) -> PackPayload? {
        try? JSONDecoder().decode(PackPayload.self, from: json)
    }
}
