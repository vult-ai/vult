// vult-release-touchid keeps a release signing key in this Mac's Secure
// Enclave. The private key never leaves the Secure Enclave and is used only
// after Touch ID. vult-release calls it; it is not meant to be run by hand.
//
//   vult-release-touchid create FILE
//       Creates a key. FILE is a handle only this Mac's Secure Enclave can use;
//       FILE.pub is the public key (PEM).
//   vult-release-touchid sign FILE REASON
//       Reads a message on standard input, asks for Touch ID showing REASON,
//       and writes the ECDSA P-256 signature (DER, base64) to standard output.
//
// Build: swiftc -O cmd/vult-release/touchid/vult-release-touchid.swift -o vult-release-touchid
import CryptoKit
import Foundation
import LocalAuthentication

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("vult-release-touchid: \(message)\n".utf8))
    exit(1)
}

func writeNew(_ path: String, _ data: Data, _ mode: Int) {
    let fd = open(path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, mode_t(mode))
    if fd < 0 { fail("\(path) already exists or cannot be created") }
    let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    handle.write(data)
    if fsync(fd) != 0 { fail("could not save \(path)") }
}

let args = CommandLine.arguments
guard args.count >= 3 else { fail("usage: create FILE | sign FILE REASON") }
guard SecureEnclave.isAvailable else { fail("this Mac has no Secure Enclave") }

switch args[1] {
case "create" where args.count == 3:
    var error: Unmanaged<CFError>?
    // Any enrolled finger, so changing fingerprints doesn't lose the key; no
    // password fallback.
    guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage, .biometryAny], &error) else {
        fail("could not set Touch ID protection")
    }
    do {
        let key = try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
        writeNew(args[2], key.dataRepresentation, 0o600)
        writeNew(args[2] + ".pub", Data((key.publicKey.pemRepresentation + "\n").utf8), 0o644)
    } catch {
        fail("could not create the key: \(error)")
    }
case "sign" where args.count == 4:
    guard let handle = FileManager.default.contents(atPath: args[2]) else { fail("cannot read \(args[2])") }
    let message = FileHandle.standardInput.readDataToEndOfFile()
    if message.isEmpty || message.count > 4 << 20 { fail("unexpected message size") }
    let context = LAContext()
    context.localizedReason = String(args[3].prefix(120))
    do {
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: handle, authenticationContext: context)
        let signature = try key.signature(for: message)
        print(signature.derRepresentation.base64EncodedString())
    } catch {
        fail("not signed: \(error)")
    }
default:
    fail("usage: create FILE | sign FILE REASON")
}
