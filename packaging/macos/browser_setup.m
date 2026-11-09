#import <Cocoa/Cocoa.h>
#import <Security/Security.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

static NSDictionary *readIdentity(void) {
    const char *parents[] = {"/Library", "/Library/Application Support", "/Library/Application Support/ai.vult.ledger"};
    struct stat info;
    for (size_t i = 0; i < sizeof(parents) / sizeof(parents[0]); ++i) {
        if (lstat(parents[i], &info) || !S_ISDIR(info.st_mode) || info.st_uid != 0 || (info.st_mode & 0022)) return nil;
    }
    int fd = open("/Library/Application Support/ai.vult.ledger/browser-identity.json", O_RDONLY | O_NOFOLLOW | O_NONBLOCK | O_CLOEXEC);
    if (fd < 0) return nil;
    if (fstat(fd, &info) || !S_ISREG(info.st_mode) || info.st_uid != 0 || info.st_nlink != 1 ||
        (info.st_mode & 0022) || info.st_size <= 0 || info.st_size > 16384) {
        close(fd);
        return nil;
    }
    NSMutableData *data = [NSMutableData dataWithLength:(NSUInteger)info.st_size];
    size_t offset = 0;
    while (offset < data.length) {
        ssize_t count = read(fd, (char *)data.mutableBytes + offset, data.length - offset);
        if (count <= 0) {
            close(fd);
            return nil;
        }
        offset += (size_t)count;
    }
    struct stat after;
    BOOL unchanged = !fstat(fd, &after) && info.st_dev == after.st_dev && info.st_ino == after.st_ino &&
        info.st_mode == after.st_mode && info.st_nlink == after.st_nlink && info.st_size == after.st_size &&
        info.st_mtimespec.tv_sec == after.st_mtimespec.tv_sec && info.st_mtimespec.tv_nsec == after.st_mtimespec.tv_nsec &&
        info.st_ctimespec.tv_sec == after.st_ctimespec.tv_sec && info.st_ctimespec.tv_nsec == after.st_ctimespec.tv_nsec;
    close(fd);
    if (!unchanged) return nil;
    NSDictionary *identity = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
    if (![identity isKindOfClass:NSDictionary.class] || identity.count != 3 ||
        ![identity[@"format"] isEqual:@"vult-browser-trust-v1"] ||
        ![identity[@"hostname"] isKindOfClass:NSString.class] ||
        ![identity[@"certificate_der"] isKindOfClass:NSString.class]) return nil;
    NSString *host = identity[@"hostname"];
    if (![host isEqualToString:@"vult.localhost"]) return nil;
    return identity;
}

static BOOL verifyCertificate(SecCertificateRef certificate, NSString *host, BOOL explicitAnchor) {
    SecPolicyRef policy = SecPolicyCreateSSL(true, (__bridge CFStringRef)host);
    SecTrustRef trust = NULL;
    OSStatus status = SecTrustCreateWithCertificates(certificate, policy, &trust);
    CFRelease(policy);
    if (status != errSecSuccess) return NO;
    status = SecTrustSetNetworkFetchAllowed(trust, false);
    if (status == errSecSuccess && explicitAnchor) {
        status = SecTrustSetAnchorCertificates(trust, (__bridge CFArrayRef)@[(__bridge id)certificate]);
        if (status == errSecSuccess) status = SecTrustSetAnchorCertificatesOnly(trust, true);
    }
    BOOL valid = status == errSecSuccess && SecTrustEvaluateWithError(trust, NULL);
    CFRelease(trust);
    return valid;
}

// Chrome ignores macOS trust settings limited to a hostname, so the trust is
// limited to SSL only. The certificate itself cannot sign other certificates
// and names only this installation's address.
static BOOL hostnameLimitedTrust(SecCertificateRef certificate) {
    CFArrayRef settings = NULL;
    if (SecTrustSettingsCopyTrustSettings(certificate, kSecTrustSettingsDomainAdmin, &settings) != errSecSuccess || !settings) return NO;
    BOOL limited = NO;
    for (NSDictionary *setting in (__bridge NSArray *)settings) {
        if ([setting isKindOfClass:NSDictionary.class] && setting[(__bridge NSString *)kSecTrustSettingsPolicyString]) limited = YES;
    }
    CFRelease(settings);
    return limited;
}

@interface BrowserSetup : NSObject <NSApplicationDelegate, NSWindowDelegate>
@property(nonatomic) SecCertificateRef certificate;
@property(nonatomic, copy) NSString *hostname;
@property(nonatomic, strong) NSWindow *window;
@end

@implementation BrowserSetup
- (void)windowWillClose:(NSNotification *)notification {
    (void)notification;
    exit(1);
}
- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    (void)notification;
    self.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 480, 160)
        styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable backing:NSBackingStoreBuffered defer:NO];
    self.window.title = @"Vult Browser Setup";
    self.window.delegate = self;
    NSTextField *label = [NSTextField wrappingLabelWithString:[NSString stringWithFormat:
        @"Allow encrypted browser access to Vult on this Mac. Approve the macOS authorization dialog to continue.\n\n%@", self.hostname]];
    label.frame = NSMakeRect(24, 24, 432, 112);
    [self.window.contentView addSubview:label];
    [self.window center];
    [self.window makeKeyAndOrderFront:nil];
    [NSApp activate];
    dispatch_async(dispatch_get_main_queue(), ^{
        SecPolicyRef policy = SecPolicyCreateSSL(true, NULL);
        NSDictionary *constraint = @{
            (__bridge NSString *)kSecTrustSettingsPolicy: (__bridge id)policy,
            (__bridge NSString *)kSecTrustSettingsResult: @(kSecTrustSettingsResultTrustRoot)
        };
        OSStatus status = SecTrustSettingsSetTrustSettings(self.certificate, kSecTrustSettingsDomainAdmin,
                                                         (__bridge CFTypeRef)@[constraint]);
        CFRelease(policy);
        BOOL valid = status == errSecSuccess && verifyCertificate(self.certificate, self.hostname, NO);
        if (!valid) fputs("Vult browser certificate approval did not complete.\n", stderr);
        exit(valid ? 0 : 1);
    });
}
@end

int main(void) {
    @autoreleasepool {
        if (getuid() == 0 || geteuid() != getuid()) return 1;
        NSDictionary *identity = readIdentity();
        if (!identity) return 1;
        NSData *der = [[NSData alloc] initWithBase64EncodedString:identity[@"certificate_der"] options:0];
        SecCertificateRef certificate = der ? SecCertificateCreateWithData(NULL, (__bridge CFDataRef)der) : NULL;
        if (!certificate) return 1;
        NSString *host = identity[@"hostname"];
        if (!verifyCertificate(certificate, host, YES)) {
            CFRelease(certificate);
            return 1;
        }
        if (verifyCertificate(certificate, host, NO) && !hostnameLimitedTrust(certificate)) {
            CFRelease(certificate);
            return 0;
        }
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        __attribute__((objc_precise_lifetime)) BrowserSetup *delegate = [BrowserSetup new];
        delegate.certificate = certificate;
        delegate.hostname = host;
        NSApp.delegate = delegate;
        [NSApp run];
        CFRelease(certificate);
    }
    return 1;
}
