buildscript {
    dependencies {
        // AGP's built-in Kotlin support uses whichever Kotlin Gradle plugin is
        // on the classpath; pin it so the compiler matches the Compose plugin.
        classpath(libs.kotlin.gradle.plugin)
    }
}

plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.compose) apply false
}
