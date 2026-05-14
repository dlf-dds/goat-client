// goat-client Android shell — :app module.
//
// Wraps the gomobile-bound goat-client.aar (built from
// ../../GoatClientSDK with `gomobile bind`; see
// mobile/android/README.md for the pipeline).

import java.util.Locale

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// ─── Release signing ────────────────────────────────────────────────────────
//
// Release signing reads from .envrc.local (direnv-managed; see commit
// 56b993d for the gitignore guard). Order of resolution:
//
//   storeFile      ← env ANDROID_UPLOAD_KEYSTORE
//   storePassword  ← env ANDROID_UPLOAD_KEY_PASSWORD (falls back to macOS
//                    Keychain if the env var is empty)
//   keyAlias       ← env ANDROID_UPLOAD_KEY_ALIAS
//   keyPassword    ← same as storePassword (single-password keystore)
//
// If ANDROID_UPLOAD_KEYSTORE is unset, the release build skips signing
// entirely — Gradle still produces an unsigned .apk / .aab that can be
// debug-installed locally but won't pass Play Console upload. The
// signed-AAB target is `./gradlew :app:bundleRelease`.
//
// Keychain fallback (only triggers when ANDROID_UPLOAD_KEY_PASSWORD is
// empty / unset). Keeps the password off-disk for operators who prefer
// macOS Keychain over .envrc.local plaintext.
val keystorePath: String? = System.getenv("ANDROID_UPLOAD_KEYSTORE")
val keyAliasEnv: String = System.getenv("ANDROID_UPLOAD_KEY_ALIAS") ?: "goat-upload"
val keyPassword: String? = System.getenv("ANDROID_UPLOAD_KEY_PASSWORD")?.takeIf { it.isNotBlank() }
    ?: runCatching {
        // macOS Keychain fallback — `security find-generic-password -w` writes
        // the password to stdout. Empty stdout means no entry; we propagate
        // null so the build still finishes (just without signing).
        val proc = ProcessBuilder(
            "security", "find-generic-password",
            "-a", "goat-client",
            "-s", "goat-client-upload-key",
            "-w",
        ).redirectErrorStream(false).start()
        val out = proc.inputStream.bufferedReader().readText().trim()
        if (proc.waitFor() == 0 && out.isNotEmpty()) out else null
    }.getOrNull()

val canSignRelease: Boolean = keystorePath != null && file(keystorePath).exists() && keyPassword != null

android {
    namespace = "io.dlf_dds.goat_client"
    // compileSdk + targetSdk bumped to 35 (Android 15) per Google Play
    // policy 2026-Q2: new apps must target API ≥35 for Internal Testing
    // track uploads.
    compileSdk = 35

    defaultConfig {
        applicationId = "io.dlf_dds.goat_client"
        minSdk = 24      // Android 7.0 — covers >97% devices, predates the
                         // pidfd-seccomp-policy issues that the engine
                         // works around at runtime.
        targetSdk = 35
        // versionCode bumped to 2 — version 1 was already uploaded to
        // Play Internal Testing track. Play rejects re-uploads with the
        // same versionCode.
        versionCode = 2
        versionName = "0.0.1-pre"
    }

    signingConfigs {
        if (canSignRelease) {
            // Capture into non-null locals so the DSL setters (which take
            // String, not String?) get non-null values without relying on
            // smart-casts across file-scope vals.
            val ks = keystorePath!!
            val kp = keyPassword!!
            create("release") {
                storeFile = file(ks)
                storePassword = kp
                keyAlias = keyAliasEnv
                keyPassword = kp
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
            if (canSignRelease) {
                signingConfig = signingConfigs.getByName("release")
            }
            // If signing config is absent, Gradle leaves the release
            // artifact unsigned. Set ANDROID_UPLOAD_KEYSTORE +
            // ANDROID_UPLOAD_KEY_PASSWORD (or store the password in macOS
            // Keychain under service `goat-client-upload-key`) to enable
            // signing for Play Internal track uploads.
        }
        debug {
            isDebuggable = true
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        viewBinding = true
    }
    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    // The gomobile-built AAR. Resolved via the flatDir repo declared
    // in settings.gradle.kts. The file itself is .gitignore'd; build
    // it with `gomobile bind` per mobile/android/README.md.
    implementation(files("libs/goat-client.aar"))

    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-ktx:1.9.2")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.6")
    implementation("androidx.lifecycle:lifecycle-viewmodel-ktx:2.8.6")
    implementation("com.google.android.material:material:1.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")

    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
}

// Emit a one-line summary at the start of any :app task so operators see at
// a glance whether release signing is wired up. Cheap; matches the
// .envrc.local-vs-Keychain story above.
tasks.matching { it.name == "assembleRelease" || it.name == "bundleRelease" }.configureEach {
    doFirst {
        val msg = if (canSignRelease) {
            "[goat-client] release signing: enabled (keystore=${keystorePath}, alias=${keyAliasEnv})"
        } else {
            val why = when {
                keystorePath == null -> "ANDROID_UPLOAD_KEYSTORE unset"
                !file(keystorePath).exists() -> "keystore file not found at $keystorePath"
                else -> "ANDROID_UPLOAD_KEY_PASSWORD unset and Keychain lookup empty"
            }
            "[goat-client] release signing: DISABLED ($why); artifact will be unsigned"
        }
        logger.lifecycle(msg)
    }
}
