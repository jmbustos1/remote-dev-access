# El core Go (gomobile) llama a Java por JNI usando nombres de clase y método:
# R8 no puede renombrarlos ni eliminarlos.
-keep class go.** { *; }
-keep class dev.jmbustos.rda.mobile.** { *; }
