# gomobile: the Go runtime calls into these classes through JNI.
-keep class go.** { *; }
-keep class org.opentether.core.** { *; }
# Kotlin implementation of the Go Platform interface is invoked from Go.
-keep class org.opentether.app.service.AndroidPlatform { *; }
