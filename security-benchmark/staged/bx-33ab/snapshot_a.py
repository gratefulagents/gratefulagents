def execute(data):
    paid = data["allocation"] if data["eligible"] else 0
    if paid and data["reenter"]:
        paid += data["allocation"]
    return {"paid": paid}
