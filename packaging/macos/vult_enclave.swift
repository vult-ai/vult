// vult-enclave protects Vult's bank keyring with this Mac's Secure Enclave.
//
// The keyring itself is sealed with a key derived from a Secure Enclave key
// that never leaves the chip. The enclave key's handle is stored as a file,
// which only this Mac's Secure Enclave can use: a copied file, disk or backup
// opens nowhere else. No keychain, entitlement or user presence is involved,
// so the background service can use it.
//
//   vult-enclave available               exit 0 when this Mac has a Secure Enclave
//   vult-enclave seal LEVEL KEYFILE      seal standard input to standard output,
//                                        creating KEYFILE on first use
//   vult-enclave open LEVEL KEYFILE      open standard input to standard output
//
// LEVEL is "read", which works in the background. "act" is reserved for
// actions that will need the person's presence each time; it is refused.
import CryptoKit
import Foundation

enum Exit: Int32 { case usage = 2, unavailable = 3, reserved = 4, failed = 5 }

func stop(_ code: Exit, _ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(code.rawValue)
}

let args = CommandLine.arguments
if args.count == 2 && args[1] == "available" {
    exit(SecureEnclave.isAvailable ? 0 : Exit.unavailable.rawValue)
}
guard args.count == 4, args[1] == "seal" || args[1] == "open" else {
    stop(.usage, "usage: vult-enclave available | seal|open LEVEL KEYFILE")
}
guard args[2] == "read" else {
    stop(.reserved, "the \(args[2]) level is reserved and needs the person's presence")
}
guard SecureEnclave.isAvailable else { stop(.unavailable, "this Mac has no Secure Enclave") }
let keyFile = URL(fileURLWithPath: args[3])
let info = Data("vult-keyring-\(args[2])".utf8)
let input = FileHandle.standardInput.readDataToEndOfFile()
guard input.count <= 1 << 20 else { stop(.usage, "input too large") }

/// The reason the last system call failed, as the system says it.
func reason() -> String { String(cString: strerror(errno)) }

/// Writes the enclave key's handle beside KEYFILE and moves it into place,
/// stopping with the system's reason when the folder won't take it.
func saveKey(_ data: Data) {
    let folder = keyFile.deletingLastPathComponent().path
    let temporary = folder + "/.enclave-\(UUID().uuidString)"
    let fd = open(temporary, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0o600)
    guard fd >= 0 else { stop(.failed, "the enclave key couldn't be created in \(folder): \(reason())") }
    let written = data.withUnsafeBytes { write(fd, $0.baseAddress, data.count) }
    let synced = written == data.count && fsync(fd) == 0
    let why = synced ? "" : reason()
    close(fd)
    guard synced else {
        unlink(temporary)
        stop(.failed, "the enclave key couldn't be written in \(folder): \(why)")
    }
    guard rename(temporary, keyFile.path) == 0 else {
        let why = reason()
        unlink(temporary)
        stop(.failed, "the enclave key couldn't be saved as \(keyFile.path): \(why)")
    }
}

func enclaveKey(create: Bool) -> SecureEnclave.P256.KeyAgreement.PrivateKey {
    if let data = try? Data(contentsOf: keyFile) {
        do { return try SecureEnclave.P256.KeyAgreement.PrivateKey(dataRepresentation: data) } catch { stop(.failed, "the enclave key can't be used on this Mac") }
    }
    guard create else { stop(.failed, "no enclave key") }
    var error: Unmanaged<CFError>?
    // Usable by the background service after the Mac first unlocks; never
    // migrates to another device.
    guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly, .privateKeyUsage, &error) else {
        stop(.failed, "access control unavailable")
    }
    do {
        let key = try SecureEnclave.P256.KeyAgreement.PrivateKey(accessControl: access)
        saveKey(key.dataRepresentation)
        return key
    } catch {
        stop(.failed, "the enclave key couldn't be created")
    }
}

func kek(_ key: SecureEnclave.P256.KeyAgreement.PrivateKey, _ peer: P256.KeyAgreement.PublicKey) -> SymmetricKey {
    guard let shared = try? key.sharedSecretFromKeyAgreement(with: peer) else { stop(.failed, "key agreement failed") }
    return shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: Data(), sharedInfo: info, outputByteCount: 32)
}

if args[1] == "seal" {
    let key = enclaveKey(create: true)
    let ephemeral = P256.KeyAgreement.PrivateKey()
    guard let box = try? AES.GCM.seal(input, using: kek(key, ephemeral.publicKey), authenticating: info), let combined = box.combined else {
        stop(.failed, "sealing failed")
    }
    FileHandle.standardOutput.write(Data([1]) + ephemeral.publicKey.x963Representation + combined)
} else {
    guard input.count > 1 + 65 + 28, input.first == 1 else { stop(.failed, "not a sealed keyring") }
    let key = enclaveKey(create: false)
    guard let peer = try? P256.KeyAgreement.PublicKey(x963Representation: input.subdata(in: 1..<66)),
          let box = try? AES.GCM.SealedBox(combined: input.subdata(in: 66..<input.count)),
          let plain = try? AES.GCM.open(box, using: kek(key, peer), authenticating: info) else {
        stop(.failed, "the keyring can't be opened on this Mac")
    }
    FileHandle.standardOutput.write(plain)
}
