package torznab

import "encoding/xml"

type newznabError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

type caps struct {
	XMLName    xml.Name  `xml:"caps"`
	Server     server    `xml:"server"`
	Limits     limits    `xml:"limits"`
	Searching  searching `xml:"searching"`
	Categories []cat     `xml:"categories>category"`
}

type server struct {
	Title string `xml:"title,attr"`
}

type limits struct {
	Default int `xml:"default,attr"`
	Max     int `xml:"max,attr"`
}

type searching struct {
	Modes []mode
}

// mode is one search function; its element name is the function's.
type mode struct {
	XMLName   xml.Name
	Available string `xml:"available,attr"`
	Params    string `xml:"supportedParams,attr"`
}

type cat struct {
	ID      int      `xml:"id,attr"`
	Name    string   `xml:"name,attr"`
	Subcats []subcat `xml:"subcat"`
}

type subcat struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

func capabilities() caps {
	return caps{
		Server: server{Title: "sniffarr"},
		Limits: limits{Default: DefaultLimit, Max: MaxLimit},
		Searching: searching{Modes: []mode{
			{XMLName: xml.Name{Local: "search"}, Available: "yes", Params: "q"},
			{XMLName: xml.Name{Local: "tv-search"}, Available: "yes", Params: "q,tvdbid,tmdbid,season,ep"},
			{XMLName: xml.Name{Local: "movie-search"}, Available: "yes", Params: "q,tmdbid,imdbid"},
			{XMLName: xml.Name{Local: "music-search"}, Available: "no", Params: ""},
			{XMLName: xml.Name{Local: "audio-search"}, Available: "no", Params: ""},
			{XMLName: xml.Name{Local: "book-search"}, Available: "no", Params: ""},
		}},
		Categories: []cat{
			{ID: catMovies, Name: "Movies", Subcats: []subcat{{catMoviesSD, "Movies/SD"}, {catMoviesHD, "Movies/HD"}, {catMoviesUHD, "Movies/UHD"}}},
			{ID: catTV, Name: "TV", Subcats: []subcat{{catTVSD, "TV/SD"}, {catTVHD, "TV/HD"}, {catTVUHD, "TV/UHD"}}},
		},
	}
}

type feed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Atom    string   `xml:"xmlns:atom,attr"`
	Torznab string   `xml:"xmlns:torznab,attr"`
	Channel channel  `xml:"channel"`
}

func newFeed() *feed {
	return &feed{Version: "2.0", Atom: "http://www.w3.org/2005/Atom", Torznab: "http://torznab.com/schemas/2015/feed",
		Channel: channel{Title: "sniffarr", Description: "BitTorrent DHT releases"}}
}

type channel struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Items       []item `xml:"item"`
}

type item struct {
	Title     string    `xml:"title"`
	GUID      guid      `xml:"guid"`
	Link      string    `xml:"link"`
	PubDate   string    `xml:"pubDate"`
	Size      int64     `xml:"size"`
	Category  string    `xml:"category"`
	Enclosure enclosure `xml:"enclosure"`
	Attrs     []attr    `xml:"torznab:attr"`
}

type guid struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}
