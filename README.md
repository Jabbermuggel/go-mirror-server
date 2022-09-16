# Proof of concept for pacoloco download rework

The pacoloco (arch mirror software) way of downloading has multiple drawbacks, including serving half-downloaded files 
as if they were complete and not allowing byte range downloads. This is due to a relatively simple implementation of 
the download process, consisting only of downloading and writing the file both to disk and the client that asked for it.
This means that a new client asking for this file will get treated no differently than if the file had already been cached,
resulting in the behaviour described in [issue 30](https://github.com/anatol/pacoloco/issues/30).

This code circumvents that problem by separating the download process from the response (download being managed by 
[grab](https://github.com/cavaliergopher/grab), although that may even be replaceable). Clients then get served the 
unfinished file over a custom file reader, which wraps the file object and stops read attempts on bytes not yet written.
This allows multiple clients to request the same file while it is being downloaded by the mirror software. It also 
allows for byte range requests, since the http.ServeContent-function which can now be used to serve the files supports this
by default.